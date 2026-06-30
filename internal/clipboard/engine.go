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
	"os"
	"slices"
	"strings"
	"time"

	"github.com/deploymenttheory/weave/internal/clipboard/macpb"
	"github.com/deploymenttheory/weave/internal/clipboard/wire"
	"github.com/deploymenttheory/weave/internal/clipboardpolicy"
	"github.com/deploymenttheory/weave/internal/guestweaveagent/agentbin"
	guestclient "github.com/deploymenttheory/weave/internal/guestweaveagent/client"
	"github.com/deploymenttheory/weave/internal/guestweaveagent/proto"
	"github.com/deploymenttheory/weave/internal/logging"
	"github.com/deploymenttheory/weave/internal/macaddress"
	weavessh "github.com/deploymenttheory/weave/internal/ssh"
	"github.com/deploymenttheory/weave/internal/vmdirectory"

	mainthread "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/custom/mainthread"
)

const (
	pollInterval      = 1 * time.Second
	backoffInterval   = 5 * time.Second
	errorLogThreshold = 3
)

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
	policy             clipboardpolicy.Policy
	vmName             string
	vmDir              *vmdirectory.VMDirectory
	mac                macaddress.MACAddress
	user, password     string
	guestOS, guestArch string

	allowedSet  map[wire.Canonical]bool
	allowedList []wire.Canonical
	limiter     *limiter
	stageDir    string // host staging dir for files applied to the host pasteboard

	// Loop-prevention state.
	lastHostChangeCount  uint64
	lastGuestChangeCount uint64
	lastHostHash         string
	lastGuestHash        string

	// Agent connection, invalidated on error or IP change.
	client   *guestclient.Client
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

// NewEngine builds a clipboard engine for one VM. guestOS/guestArch select the
// agent binary to deploy (e.g. "darwin"/"arm64", "linux"/"amd64").
func NewEngine(policy clipboardpolicy.Policy, vmName string, vmDir *vmdirectory.VMDirectory, mac macaddress.MACAddress, user, password, guestOS, guestArch string) *Engine {
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

	e.allowedSet = e.policy.AllowedCanonical()
	e.allowedList = sortedCanonicals(e.allowedSet)
	e.limiter = newLimiter(e.policy.BytesPerSec())
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

func (e *Engine) sync(ctx context.Context) {
	defer e.publishHealth() // refresh the status panel every cycle

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
			switch {
			case payload.Empty() || hash == e.lastHostHash || hash == e.lastGuestHash:
				e.lastHostChangeCount = hostCC // nothing new to push; accept the count
			default:
				if err := e.setGuest(ctx, payload); err != nil {
					e.handleError("sync clipboard to guest", err) // leave state for retry
				} else {
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
		if !payload.Empty() && hash != e.lastGuestHash && hash != e.lastHostHash {
			e.applyHost(payload)
			e.lastGuestHash = hash
			e.lastHostHash = hash
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

	raw, _ := json.Marshal(wire.Meta{Allowed: e.allowedList})
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
func (e *Engine) readMeta(c *guestclient.Client) (wire.Meta, error) {
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

// agent returns a connected guest agent client for the VM's current IP, or nil
// when the VM is not running, has no resolvable IP yet, or the agent cannot be
// deployed (silent skip — the VM may still be booting).
func (e *Engine) agent(ctx context.Context) *guestclient.Client {
	if e.disabled {
		return nil
	}
	// The engine runs inside the "weave run" process, so the VM is up for as long
	// as ctx is live. Don't probe vmDir.Running() here: it opens config.json (the
	// PID-lock file), and on macOS closing that descriptor drops the run
	// process's own fcntl lock (POSIX releases a process's locks when it closes
	// any descriptor to the file), making the VM misreport as stopped/suspended.
	if ctx.Err() != nil {
		return nil
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

	if e.client != nil && e.cachedIP == ip.String() {
		return e.client
	}
	if e.client != nil {
		_ = e.client.Close()
		e.client = nil
	}

	ssh := weavessh.NewSSHClient(ip.String(), 22, e.user, e.password)
	client, err := guestclient.Dial(ctx, ssh, guestclient.Options{GOOS: e.guestOS, GOARCH: e.guestArch, Password: e.password})
	if err != nil {
		e.hConnected = false
		e.hAgentVersion = ""
		e.recordDialError(err)
		if strings.Contains(err.Error(), "no embedded agent") {
			e.disabled = true
			logging.DefaultLogger().AppendNewLine("Clipboard disabled: " + err.Error())
			e.publishHealth()
			return nil
		}
		e.handleError("connect guest agent", err)
		e.publishHealth()
		return nil
	}
	e.client = client
	e.cachedIP = ip.String()
	e.hSSHOK = true
	e.hSSHDetail = fmt.Sprintf("%s@%s", e.user, ip.String())
	e.hConnected = true
	e.hAgentVersion = agentVersionLabel(client)
	e.resetFailure()
	e.publishHealth()
	return client
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
func agentVersionLabel(c *guestclient.Client) string {
	if c.Hello.Version == "" {
		return "connected"
	}
	return fmt.Sprintf("%s (%s/%s)", c.Hello.Version, c.Hello.OS, c.Hello.Arch)
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
		{Label: "SSH / Remote Login reachable", OK: e.hSSHOK, Detail: sshDetail},
		{Label: "Guest agent (weave-guestd) connected", OK: e.hConnected, Detail: e.hAgentVersion},
		{Label: "Last clipboard sync", OK: e.hLastErr == "" && e.hConnected, Detail: syncDetail},
	}

	e.reporter(Health{Enabled: e.policy.Active(), Summary: e.summary(), Checks: checks})
}

// summary renders the resolved policy as one line, e.g. "bidirectional · text".
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
	return fmt.Sprintf("%s · %s", e.policy.Direction, f)
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
