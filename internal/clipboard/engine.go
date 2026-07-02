// Package clipboard is the host side of weave's enterprise clipboard: a
// policy-driven engine that mirrors the host (macOS) and guest clipboards over
// the weave guest agent. It enforces directionality, per-format allow-lists,
// independent file transfer, a per-item size cap, and a bandwidth limit
// expressed as a percentage of declared session bandwidth — preserving rich
// text and images, not just plain text.
//
// The engine reads/writes the host NSPasteboard via the shared macpb package on
// the main thread, and drives the guest's clipboard module through the agent
// client. It supersedes the original lume-style text-only SSH watcher.
//go:build darwin

package clipboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"

	"github.com/deploymenttheory/guestweave/internal/clipboard/macpb"
	"github.com/deploymenttheory/guestweave/internal/clipboard/wire"
	"github.com/deploymenttheory/guestweave/internal/clipboardpolicy"
	"github.com/deploymenttheory/guestweave/internal/guestweaveagent/agentbin"
	guestclient "github.com/deploymenttheory/guestweave/internal/guestweaveagent/client"
	"github.com/deploymenttheory/guestweave/internal/guestweaveagent/proto"
	"github.com/deploymenttheory/guestweave/internal/logging"
	"github.com/deploymenttheory/guestweave/internal/macaddress"
	weavessh "github.com/deploymenttheory/guestweave/internal/ssh"
	"github.com/deploymenttheory/guestweave/internal/vm/layout"

	mainthread "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/tools/grandcentraldispatch/mainthread"
)

const (
	pollInterval      = 1 * time.Second
	backoffInterval   = 5 * time.Second
	errorLogThreshold = 3
)

// clipDebug reports whether verbose per-cycle sync tracing to stderr is
// enabled (GUESTWEAVE_CLIPBOARD_DEBUG). Used to diagnose host⇄guest
// conflict/ordering issues. Read lazily — the config layer initialises after
// package init.
func clipDebug() bool { return weaveconfig.ClipboardDebug() }

func dbg(format string, a ...any) {
	if clipDebug() {
		fmt.Fprintf(os.Stderr, "[clipdbg] "+format+"\n", a...)
	}
}

// short truncates a hash for debug logs.
func short(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	if h == "" {
		return "<none>"
	}
	return h
}

// summarize renders a payload's shape for debug logs.
func summarize(p wire.Payload) string {
	var b strings.Builder
	for _, it := range p.Items {
		fmt.Fprintf(&b, "item(%s,%dB) ", it.Format, len(it.Data))
	}
	for _, f := range p.Files {
		fmt.Fprintf(&b, "file(%s,%dB) ", f.Name, len(f.Data))
	}
	if b.Len() == 0 {
		return "<empty>"
	}
	return strings.TrimSpace(b.String())
}

// StartedMarker is the stable prefix the engine prints to stdout once its sync
// loop is live. Tooling (the acceptance harness) waits for this exact string to
// know the clipboard engine has started, so keep it in sync with the Run log.
const StartedMarker = "Clipboard policy engine started"

// Check is one clipboard prerequisite and its live status, surfaced in the UI's
// Clipboard Status panel (green when OK).
type Check struct {
	Label  string
	OK     bool
	Detail string
}

// Health is the engine's live self-diagnosis: the resolved policy plus a check
// for each thing that must be true for clipboard to work (embedded agent, guest
// IP, SSH/Remote Login, agent connected, last sync). The UI renders it as a
// checklist so a user can see at a glance what is missing.
type Health struct {
	Enabled bool   // the policy intends clipboard to be on
	Summary string // one-line resolved policy (direction · formats)
	Checks  []Check
}

// AllOK reports whether every check passed.
func (h Health) AllOK() bool {
	for _, c := range h.Checks {
		if !c.OK {
			return false
		}
	}
	return len(h.Checks) > 0
}

// Engine mirrors the clipboard between the host and one VM under a policy.
type Engine struct {
	// policy and its derived state (allowedSet/allowedList/limiter/auditOn) are
	// mutated only on the sync goroutine. A live update via SetPolicy queues the
	// new policy in pendingPolicy (guarded by mu); the sync loop applies it at the
	// top of the next cycle, so the derived state never changes mid-cycle.
	policy             clipboardpolicy.Policy
	mu                 sync.Mutex
	pendingPolicy      *clipboardpolicy.Policy
	vmName             string
	vmDir              *layout.VMDirectory
	mac                macaddress.MACAddress
	user, password     string
	guestOS, guestArch string

	allowedSet  map[wire.Canonical]bool
	allowedList []wire.Canonical
	limiter     *limiter
	auditOn     bool   // structured transfer auditing enabled (policy or env)
	stageDir    string // host staging dir for files applied to the host pasteboard

	// Loop-prevention state.
	lastHostChangeCount  uint64
	lastGuestChangeCount uint64
	lastHostHash         string
	lastGuestHash        string

	// Resident guest agent over a virtio serial channel. serialR/serialW are the
	// host ends of the bridge (set by the run command before Run); serial is the
	// live connection, created once the resident agent has been installed and
	// started. installed gates the one-time SSH install.
	serialR   io.Reader
	serialW   io.Writer
	serial    *guestclient.SerialConn
	installed bool

	// Active agent connection (== serial once ready), invalidated on error.
	client   guestclient.Conn
	cachedIP string

	// Error suppression.
	consecutiveFailures int
	lastLoggedError     string
	disabled            bool // permanently off (e.g. no embedded agent for the guest)

	// Live health, surfaced to the UI via reporter (set before Run).
	reporter      func(Health)
	hIPResolved   bool
	hIP           string
	hSSHOK        bool
	hSSHDetail    string
	hConnected    bool
	hAgentVersion string
	hLastErr      string
}

// SetReporter registers a callback the engine invokes with a fresh Health
// snapshot whenever its connection/sync state changes. Call before Run.
func (e *Engine) SetReporter(fn func(Health)) { e.reporter = fn }

// SetSerialChannel wires the host ends of the resident agent's virtio serial
// channel (built by the run command). Call before Run. Without it the engine has
// no transport and stays disabled.
func (e *Engine) SetSerialChannel(r io.Reader, w io.Writer) {
	e.serialR = r
	e.serialW = w
}

// NewEngine builds a clipboard engine for one VM. guestOS/guestArch select the
// agent binary to deploy (e.g. "darwin"/"arm64", "linux"/"amd64").
func NewEngine(policy clipboardpolicy.Policy, vmName string, vmDir *layout.VMDirectory, mac macaddress.MACAddress, user, password, guestOS, guestArch string) *Engine {
	return &Engine{
		policy:    policy,
		vmName:    vmName,
		vmDir:     vmDir,
		mac:       mac,
		user:      user,
		password:  password,
		guestOS:   guestOS,
		guestArch: guestArch,
	}
}

// Run mirrors the clipboard until ctx is cancelled. Call as a goroutine after
// the VM starts. It returns immediately when the policy is inactive.
func (e *Engine) Run(ctx context.Context) {
	if !e.policy.Active() {
		return
	}

	e.recomputeDerived()
	if dir, err := os.MkdirTemp("", "weave-clip-host-"); err == nil {
		e.stageDir = dir
	}
	e.initHostState()

	fmt.Printf("%s (direction=%s, files=%v, formats=%v)\n",
		StartedMarker, e.policy.Direction, e.policy.FileTransfer, e.allowedList)

	e.publishHealth() // initial: policy is set; connection checks fill in as we sync

	for {
		interval := pollInterval
		if e.consecutiveFailures >= errorLogThreshold {
			interval = backoffInterval
		}
		select {
		case <-ctx.Done():
			if e.client != nil {
				_ = e.client.Close()
			}
			return
		case <-time.After(interval):
		}
		e.sync(ctx)
	}
}

// initHostState seeds the change-count and hash state from the current host
// clipboard so nothing is synced at startup.
func (e *Engine) initHostState() {
	var payload wire.Payload
	mainthread.Do(func() {
		e.lastHostChangeCount = macpb.ChangeCount()
		payload = macpb.Read(e.allowedSet, e.policy.MaxBytes())
	})
	hash := hashPayload(payload)
	e.lastHostHash = hash
	e.lastGuestHash = hash // assume the guest starts matching the host
}

// recomputeDerived rebuilds the per-cycle state derived from e.policy. Called
// only on the sync goroutine (Run init and applyPendingPolicy).
func (e *Engine) recomputeDerived() {
	e.allowedSet = e.policy.AllowedCanonical()
	e.allowedList = sortedCanonicals(e.allowedSet)
	e.limiter = newLimiter(e.policy.BytesPerSec())
	e.auditOn = e.policy.AuditLog || weaveconfig.ClipboardAudit()
}

// SetPolicy requests a live policy change. The new policy is queued and applied
// at the start of the next sync cycle (within one poll interval), keeping all
// derived-state mutation on the sync goroutine. Safe to call from any goroutine
// (the control listener, the UI). A no-op if the engine never started.
func (e *Engine) SetPolicy(p clipboardpolicy.Policy) {
	e.mu.Lock()
	e.pendingPolicy = &p
	e.mu.Unlock()
}

// Policy returns the engine's current effective policy (the last applied one,
// not a not-yet-applied pending change). Safe to call from any goroutine.
func (e *Engine) Policy() clipboardpolicy.Policy {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pendingPolicy != nil {
		return *e.pendingPolicy
	}
	return e.policy
}

// applyPendingPolicy applies a queued live policy change, recomputing derived
// state and auditing the delta. Called only at the top of a sync cycle.
func (e *Engine) applyPendingPolicy() {
	e.mu.Lock()
	pending := e.pendingPolicy
	e.pendingPolicy = nil
	e.mu.Unlock()
	if pending == nil {
		return
	}

	old := e.summary()
	e.policy = *pending
	e.recomputeDerived()
	now := e.summary()
	logging.LogInfo("Clipboard policy updated for %s: %s → %s", e.vmName, old, now)
	e.auditPolicyChange(now)
	e.publishHealth()
}

func (e *Engine) sync(ctx context.Context) {
	defer e.publishHealth() // refresh the status panel every cycle

	e.applyPendingPolicy()

	client := e.agent(ctx)
	if client == nil {
		return
	}

	didHostToGuest := false

	// Host → guest. Dedup/loop-prevention state (change count + hashes) is only
	// advanced once the push actually succeeds, so a push that fails because the
	// guest is briefly unavailable (e.g. the guest sits at the macOS login window
	// just after boot) is retried on a later cycle instead of being silently
	// marked as already synced.
	if e.policy.AllowHostToGuest() {
		hostCC := hostChangeCount()
		if hostCC != e.lastHostChangeCount {
			payload := e.captureHost()
			hash := hashPayload(payload)
			dbg("H→G: hostCC %d→%d captured=%s hash=%s lastHost=%s lastGuest=%s",
				e.lastHostChangeCount, hostCC, summarize(payload), hash[:8], short(e.lastHostHash), short(e.lastGuestHash))
			switch {
			case payload.Empty() || hash == e.lastHostHash || hash == e.lastGuestHash:
				dbg("H→G: skip (empty=%v matchHost=%v matchGuest=%v)", payload.Empty(), hash == e.lastHostHash, hash == e.lastGuestHash)
				e.lastHostChangeCount = hostCC // nothing new to push; accept the count
			default:
				if err := e.setGuest(ctx, payload); err != nil {
					dbg("H→G: setGuest ERR: %v", err)
					e.handleError("sync clipboard to guest", err) // leave state for retry
				} else {
					dbg("H→G: pushed ok")
					e.auditTransfer(auditHostToGuest, payload)
					e.lastHostChangeCount = hostCC
					e.lastHostHash = hash
					e.lastGuestHash = hash
					e.resetFailure()
					didHostToGuest = true
				}
			}
		}
	}

	// Guest → host (skip if we just pushed: the guest's change count would race,
	// or if a failed push dropped the client mid-cycle — next cycle redials). As
	// above, the guest change count is only advanced once the fetch+apply
	// succeeds, so a transient failure is retried rather than skipped.
	if !didHostToGuest && e.policy.AllowGuestToHost() && e.client != nil {
		guestCC, err := e.statGuest()
		if err != nil {
			e.handleError("stat guest clipboard", err)
			return
		}
		if guestCC == e.lastGuestChangeCount {
			e.resetFailure()
			return
		}

		payload, err := e.getGuest(ctx)
		if err != nil {
			e.handleError("sync clipboard from guest", err) // leave count for retry
			return
		}
		hash := hashPayload(payload)
		dbg("G→H: guestCC %d→%d got=%s hash=%s lastHost=%s lastGuest=%s",
			e.lastGuestChangeCount, guestCC, summarize(payload), hash[:8], short(e.lastHostHash), short(e.lastGuestHash))
		if !payload.Empty() && hash != e.lastGuestHash && hash != e.lastHostHash {
			dbg("G→H: applying to host")
			e.applyHost(payload)
			e.auditTransfer(auditGuestToHost, payload)
			e.lastGuestHash = hash
			e.lastHostHash = hash
		} else {
			dbg("G→H: skip apply (empty=%v matchHost=%v matchGuest=%v)", payload.Empty(), hash == e.lastHostHash, hash == e.lastGuestHash)
		}
		e.lastGuestChangeCount = guestCC
		e.resetFailure()
	}
}

// ── Host pasteboard (main thread) ────────────────────────────────────────────

func hostChangeCount() uint64 {
	var cc uint64
	mainthread.Do(func() { cc = macpb.ChangeCount() })
	return cc
}

func (e *Engine) captureHost() wire.Payload {
	var payload wire.Payload
	mainthread.Do(func() {
		payload = macpb.Read(e.allowedSet, e.policy.MaxBytes())
	})
	return payload
}

func (e *Engine) applyHost(payload wire.Payload) {
	mainthread.Do(func() {
		_ = macpb.Write(payload, e.stageDir)
		e.lastHostChangeCount = macpb.ChangeCount()
	})
}

// ── Guest agent protocol ─────────────────────────────────────────────────────

// errNoClient guards the protocol methods against a connection dropped earlier
// in the same sync cycle (e.g. a failed host→guest push).
var errNoClient = fmt.Errorf("guest agent not connected")

func (e *Engine) statGuest() (uint64, error) {
	c := e.client
	if c == nil {
		return 0, errNoClient
	}
	c.Lock()
	defer c.Unlock()

	if err := proto.WriteRequest(c.Writer(), proto.Request{Module: wire.Module, Op: wire.OpStat}); err != nil {
		return 0, e.dropClient(err)
	}
	meta, err := e.readMeta(c)
	if err != nil {
		return 0, err
	}
	return meta.ChangeCount, nil
}

func (e *Engine) getGuest(ctx context.Context) (wire.Payload, error) {
	c := e.client
	if c == nil {
		return wire.Payload{}, errNoClient
	}
	c.Lock()
	defer c.Unlock()

	raw, _ := json.Marshal(wire.Meta{Allowed: e.allowedList, MaxBytes: e.policy.MaxBytes()})
	if err := proto.WriteRequest(c.Writer(), proto.Request{Module: wire.Module, Op: wire.OpGet, Meta: raw}); err != nil {
		return wire.Payload{}, e.dropClient(err)
	}
	meta, err := e.readMeta(c)
	if err != nil {
		return wire.Payload{}, err
	}
	payload, err := wire.ReadBody(c.Reader(), meta, e.gate(ctx))
	if err != nil {
		return wire.Payload{}, e.dropClient(err)
	}
	// Authoritative cap on receive: a guest that ignores the advertised MaxBytes
	// (old, buggy, or compromised) must not push an oversize item onto the host.
	payload, dropped := payload.CapTo(e.policy.MaxBytes())
	for _, d := range dropped {
		dbg("G→H: dropped oversize %s%s (%d B > cap %d)", d.Format, d.Name, d.Size, e.policy.MaxBytes())
	}
	e.auditBlocked(auditGuestToHost, "oversize", dropped)
	return payload, nil
}

func (e *Engine) setGuest(ctx context.Context, payload wire.Payload) error {
	c := e.client
	if c == nil {
		return errNoClient
	}
	c.Lock()
	defer c.Unlock()

	raw, _ := json.Marshal(wire.MetaFor(payload))
	if err := proto.WriteRequest(c.Writer(), proto.Request{Module: wire.Module, Op: wire.OpSet, Meta: raw}); err != nil {
		return e.dropClient(err)
	}
	if err := wire.WriteBody(c.Writer(), payload, e.gate(ctx)); err != nil {
		return e.dropClient(err)
	}
	if _, err := e.readMeta(c); err != nil {
		return err
	}
	return nil
}

// readMeta reads a response envelope and decodes its clipboard meta, mapping a
// transport error to a client drop and a module error to a plain error.
func (e *Engine) readMeta(c guestclient.Conn) (wire.Meta, error) {
	resp, err := proto.ReadResponse(c.Reader())
	if err != nil {
		return wire.Meta{}, e.dropClient(err)
	}
	if resp.Err != "" {
		return wire.Meta{}, fmt.Errorf("guest clipboard: %s", resp.Err)
	}
	var meta wire.Meta
	if len(resp.Meta) > 0 {
		if err := json.Unmarshal(resp.Meta, &meta); err != nil {
			return wire.Meta{}, err
		}
	}
	return meta, nil
}

// dropClient invalidates the agent connection so the next cycle redials, and
// returns the triggering error.
func (e *Engine) dropClient(err error) error {
	if e.client != nil {
		_ = e.client.Close()
		e.client = nil
		e.cachedIP = ""
	}
	e.hConnected = false
	e.hAgentVersion = ""
	return err
}

func (e *Engine) gate(ctx context.Context) wire.Gate {
	if e.limiter == nil {
		return nil
	}
	return func(n int) error { return e.limiter.waitN(ctx, n) }
}

// ── Agent connection ─────────────────────────────────────────────────────────

// agent returns a connected guest agent over the resident serial channel, or nil
// when the VM has no resolvable IP yet, the resident agent isn't installed/up
// yet, or the agent cannot be deployed (silent skip — the VM may still be
// booting or sitting at the login window).
//
// The transport is the dedicated virtio serial channel wired by the run command
// (SetSerialChannel); SSH is used only for the one-time-per-version install of
// the resident in-session agent (EnsureResident).
func (e *Engine) agent(ctx context.Context) guestclient.Conn {
	if e.disabled {
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}
	if e.serialR == nil || e.serialW == nil {
		// No serial channel wired — nothing to talk over.
		e.disabled = true
		logging.DefaultLogger().AppendNewLine("Clipboard disabled: no serial channel")
		return nil
	}

	// Already connected over serial.
	if e.client != nil {
		return e.client
	}

	ip, found, err := macaddress.ResolveIP(ctx, e.mac, macaddress.IPResolutionStrategyDHCP, 0, e.vmDir.ControlSocketURL())
	if err != nil || !found {
		e.hIPResolved = false
		e.hIP = ""
		e.hSSHOK = false
		e.hConnected = false
		e.hSSHDetail = "waiting for guest IP (DHCP lease)"
		e.publishHealth()
		return nil
	}
	e.hIPResolved = true
	e.hIP = ip.String()

	// One-time (per version): install + start the resident agent over SSH. Retried
	// each cycle until it succeeds — the guest may still be booting, SSH may be
	// down, or the user may not be logged in yet (loading a LaunchAgent into the
	// GUI session needs a console user).
	if !e.installed {
		ssh := weavessh.NewSSHClient(ip.String(), 22, e.user, e.password)
		if err := guestclient.EnsureResident(ctx, ssh, guestclient.Options{GOOS: e.guestOS, GOARCH: e.guestArch, Password: e.password}); err != nil {
			e.hConnected = false
			e.hAgentVersion = ""
			e.recordDialError(err)
			if strings.Contains(err.Error(), "no embedded agent") {
				e.disabled = true
				logging.DefaultLogger().AppendNewLine("Clipboard disabled: " + err.Error())
				e.publishHealth()
				return nil
			}
			e.handleError("install guest agent", err)
			e.publishHealth()
			return nil
		}
		e.installed = true
		e.hSSHOK = true
		e.hSSHDetail = fmt.Sprintf("%s@%s", e.user, ip.String())
	}

	// Open the serial connection once (writes a single hello); it becomes ready
	// when the resident agent answers.
	if e.serial == nil {
		e.serial = guestclient.NewSerial(e.serialR, e.serialW)
	}
	if !e.serial.Ready() {
		e.hConnected = false
		e.hAgentVersion = ""
		e.hSSHDetail = fmt.Sprintf("%s@%s (starting agent)", e.user, ip.String())
		e.publishHealth()
		return nil
	}

	e.client = e.serial
	e.hConnected = true
	e.hAgentVersion = agentVersionLabel(e.serial)
	e.resetFailure()
	e.publishHealth()
	return e.client
}

// recordDialError classifies a Dial failure for the SSH/Remote Login check: a
// refused or timed-out TCP connection means SSH (Remote Login) is not reachable;
// anything past that (auth, handshake) means SSH connected but the agent step
// failed.
func (e *Engine) recordDialError(err error) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no embedded agent"):
		e.hSSHOK = false
		e.hSSHDetail = "agent not built"
	case strings.Contains(msg, "refused"), strings.Contains(msg, "no route"),
		strings.Contains(msg, "timed out"), strings.Contains(msg, "timeout"),
		strings.Contains(msg, "connection reset"):
		e.hSSHOK = false
		e.hSSHDetail = "cannot reach SSH on " + e.hIP + ":22 — enable Remote Login in the guest"
	case strings.Contains(msg, "unable to authenticate"), strings.Contains(msg, "password"):
		e.hSSHOK = false
		e.hSSHDetail = fmt.Sprintf("auth failed for %q — check --clipboard-user/--clipboard-password", e.user)
	default:
		// TCP/auth likely succeeded; the agent deploy/handshake failed.
		e.hSSHOK = true
		e.hSSHDetail = fmt.Sprintf("%s@%s", e.user, e.hIP)
	}
}

// agentVersionLabel renders the connected agent's identity for the status panel.
func agentVersionLabel(c guestclient.Conn) string {
	h := c.HelloInfo()
	if h.Version == "" {
		return "connected"
	}
	return fmt.Sprintf("%s (%s/%s)", h.Version, h.OS, h.Arch)
}

// ── Error suppression ────────────────────────────────────────────────────────

func (e *Engine) handleError(message string, err error) {
	e.consecutiveFailures++
	desc := err.Error()
	e.hLastErr = message + ": " + desc
	if desc != e.lastLoggedError {
		logging.DefaultLogger().AppendNewLine(fmt.Sprintf("Failed to %s: %s", message, desc))
		e.lastLoggedError = desc
	} else if e.consecutiveFailures == errorLogThreshold {
		logging.DefaultLogger().AppendNewLine("Clipboard sync errors repeating, suppressing further logs until resolved")
	}
}

func (e *Engine) resetFailure() {
	if e.consecutiveFailures >= errorLogThreshold {
		logging.DefaultLogger().AppendNewLine("Clipboard sync recovered")
	}
	e.consecutiveFailures = 0
	e.lastLoggedError = ""
	e.hLastErr = ""
}

// ── Health ───────────────────────────────────────────────────────────────────

// publishHealth builds a fresh Health snapshot from the engine's current state
// and hands it to the reporter (the UI). It is called whenever connection or
// sync state changes.
func (e *Engine) publishHealth() {
	if e.reporter == nil {
		return
	}

	_, embedded := agentbin.Binary(e.guestOS, e.guestArch)
	agentDetail := fmt.Sprintf("%s/%s", e.guestOS, e.guestArch)
	if !embedded {
		agentDetail = "no embedded agent for " + agentDetail + " — build with `make build`"
	}

	sshDetail := e.hSSHDetail
	if e.hSSHOK && sshDetail == "" {
		sshDetail = fmt.Sprintf("%s@%s", e.user, e.hIP)
	}

	syncDetail := "ok"
	if e.hLastErr != "" {
		syncDetail = e.hLastErr
	}

	checks := []Check{
		{Label: "Clipboard policy enabled", OK: e.policy.Active(), Detail: e.summary()},
		{Label: "Guest agent binary embedded", OK: embedded, Detail: agentDetail},
		{Label: "Guest IP resolved", OK: e.hIPResolved, Detail: e.hIP},
		{Label: "Remote Login reachable (agent install)", OK: e.hSSHOK, Detail: sshDetail},
		{Label: "Guest agent connected (virtio serial)", OK: e.hConnected, Detail: e.hAgentVersion},
		{Label: "Last clipboard sync", OK: e.hLastErr == "" && e.hConnected, Detail: syncDetail},
	}

	e.reporter(Health{Enabled: e.policy.Active(), Summary: e.summary(), Checks: checks})
}

// summary renders the resolved policy as one line, e.g.
// "bidirectional · text, files · cap 50 MiB · audit on".
func (e *Engine) summary() string {
	formats := make([]string, 0, len(e.allowedList))
	for _, c := range e.allowedList {
		formats = append(formats, string(c))
	}
	if e.policy.FileTransfer {
		formats = append(formats, "files")
	}
	f := "none"
	if len(formats) > 0 {
		f = strings.Join(formats, ", ")
	}
	parts := []string{string(e.policy.Direction), f, "cap " + formatByteSize(e.policy.MaxBytes())}
	if e.auditOn {
		parts = append(parts, "audit on")
	}
	return strings.Join(parts, " · ")
}

// formatByteSize renders a byte count in compact IEC units for the status line
// (e.g. 52428800 → "50 MiB").
func formatByteSize(n int64) string {
	const unit = 1024
	const suffixes = "KMGTPE"
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < len(suffixes)-1; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.0f %ciB", float64(n)/float64(div), suffixes[exp])
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func hashPayload(p wire.Payload) string {
	h := sha256.New()
	for _, item := range p.Items {
		h.Write([]byte(item.Format))
		h.Write([]byte{0})
		h.Write(item.Data)
		h.Write([]byte{0})
	}
	for _, file := range p.Files {
		h.Write([]byte(file.Name))
		h.Write([]byte{0})
		h.Write(file.Data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sortedCanonicals(set map[wire.Canonical]bool) []wire.Canonical {
	list := make([]wire.Canonical, 0, len(set))
	for c := range set {
		list = append(list, c)
	}
	slices.Sort(list)
	return list
}
