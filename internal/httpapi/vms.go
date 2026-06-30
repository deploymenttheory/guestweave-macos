// VM lifecycle handlers: list, get, create, clone, update, delete, run, stop,
// suspend, rename. Each is a thin adapter over an internal/command struct or
// the shared internal/vmservice layer.
//go:build darwin

package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	weavecommand "github.com/deploymenttheory/weave/internal/command"
	"github.com/deploymenttheory/weave/internal/diskimage"
	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
	"github.com/deploymenttheory/weave/internal/vmservice"
)

func (s *APIServer) handleListVMs(w http.ResponseWriter, r *http.Request) {
	infos, err := vmservice.CollectVMInfos(r.URL.Query().Get("source"))
	if err != nil {
		writeError(w, err)
		return
	}
	if infos == nil {
		infos = []weavecommand.ListVMInfo{}
	}
	writeJSON(w, http.StatusOK, infos)
}

func (s *APIServer) handleGetVM(w http.ResponseWriter, r *http.Request) {
	details, err := vmservice.CollectVMDetails(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, details)
}

func (s *APIServer) handleCreateVM(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[createVMRequest](w, r)
	if !ok {
		return
	}
	if request.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name is required"})
		return
	}
	command := &weavecommand.CreateCommand{
		Name:       request.Name,
		Linux:      request.Linux,
		DiskSize:   50,
		DiskFormat: diskimage.DiskImageFormatRaw,
		NetProfile: request.NetProfile,
	}
	if request.DiskSizeGB != 0 {
		command.DiskSize = request.DiskSizeGB
	}
	if request.DiskFormat != "" {
		format, ok := diskimage.ParseDiskImageFormat(request.DiskFormat)
		if !ok {
			writeJSON(
				w,
				http.StatusBadRequest,
				errorResponse{Error: "unsupported disk format: " + request.DiskFormat},
			)
			return
		}
		command.DiskFormat = format
	}
	if !request.Linux {
		command.FromIPSW = request.FromIPSW
		if command.FromIPSW == "" {
			command.FromIPSW = "latest"
		}
	}
	if err := command.Validate(); err != nil {
		writeError(w, err)
		return
	}
	if err := command.Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": request.Name})
}

func (s *APIServer) handleCloneVM(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[cloneVMRequest](w, r)
	if !ok {
		return
	}
	if request.Name == "" || request.NewName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name and newName are required"})
		return
	}
	concurrency := request.Concurrency
	if concurrency == 0 {
		concurrency = 4
	}
	pruneLimit := request.PruneLimit
	if pruneLimit == 0 {
		pruneLimit = 100
	}
	command := &weavecommand.CloneCommand{
		SourceName:  request.Name,
		NewName:     request.NewName,
		Registry:    request.Registry,
		Insecure:    request.Insecure,
		Concurrency: concurrency,
		Deduplicate: request.Deduplicate,
		PruneLimit:  pruneLimit,
	}
	if err := command.Validate(); err != nil {
		writeError(w, err)
		return
	}
	if err := command.Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": request.NewName})
}

func (s *APIServer) handleUpdateVM(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[updateVMRequest](w, r)
	if !ok {
		return
	}
	command := &weavecommand.SetCommand{Name: chi.URLParam(r, "name")}
	command.CPU = request.CPU
	command.Memory = request.MemoryMB
	command.DiskSize = request.DiskSizeGB
	command.DisplayRefit = request.DisplayRefit
	command.RandomMAC = request.RandomMAC
	command.RandomSerial = request.RandomSerial
	if request.Display != nil {
		displayConfig := weavecommand.ParseVMDisplayConfig(*request.Display)
		command.Display = &displayConfig
	}
	if request.Disk != nil {
		command.Disk = *request.Disk
	}
	if err := command.Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"updated": chi.URLParam(r, "name")})
}

func (s *APIServer) handleDeleteVM(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	command := &weavecommand.DeleteCommand{Names: []string{name}}
	if err := command.Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": name})
}

func (s *APIServer) handleRunVM(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[runVMRequest](w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")

	if err := vmservice.SpawnDetachedRun(name, request.toArgs()); err != nil {
		writeError(w, err)
		return
	}
	if err := vmservice.WaitForVMRunning(r.Context(), name, 30*time.Second); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"running": name})
}

func (s *APIServer) handleStopVM(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[stopVMRequest](w, r)
	if !ok {
		return
	}
	timeout := request.Timeout
	if timeout == 0 {
		timeout = 30
	}
	name := chi.URLParam(r, "name")
	command := &weavecommand.StopCommand{Name: name, Timeout: timeout}
	if err := command.Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"stopped": name})
}

// setClipboardPolicyRequest live-updates a running VM's clipboard policy; each
// field maps to a `weave clipboard set` flag (omitted = leave unchanged).
type setClipboardPolicyRequest struct {
	Enabled      string `json:"enabled,omitempty"` // on|off
	Direction    string `json:"direction,omitempty"`
	Formats      string `json:"formats,omitempty"`
	Files        string `json:"files,omitempty"`
	AllowedTypes string `json:"allowedTypes,omitempty"`
	Audit        string `json:"audit,omitempty"`
	SessionMbps  int    `json:"sessionMbps,omitempty"`
	BandwidthPct int    `json:"bandwidthPct,omitempty"`
	MaxBytes     int64  `json:"maxBytes,omitempty"`
	Persist      bool   `json:"persist,omitempty"`
}

func (req setClipboardPolicyRequest) toArgs(name string) []string {
	args := []string{"set", name}
	addValue := func(value, flag string) {
		if value != "" {
			args = append(args, flag, value)
		}
	}
	addValue(req.Enabled, "--enabled")
	addValue(req.Direction, "--direction")
	addValue(req.Formats, "--formats")
	addValue(req.Files, "--files")
	addValue(req.AllowedTypes, "--allowed-types")
	addValue(req.Audit, "--audit")
	if req.SessionMbps != 0 {
		args = append(args, "--session-mbps", strconv.Itoa(req.SessionMbps))
	}
	if req.BandwidthPct != 0 {
		args = append(args, "--bandwidth-pct", strconv.Itoa(req.BandwidthPct))
	}
	if req.MaxBytes != 0 {
		args = append(args, "--max-bytes", strconv.FormatInt(req.MaxBytes, 10))
	}
	if req.Persist {
		args = append(args, "--persist")
	}
	return args
}

// handleSetClipboardPolicy pushes a live clipboard-policy update onto a running
// VM (POST /vms/{name}/clipboard), sharing the `weave clipboard set` path.
func (s *APIServer) handleSetClipboardPolicy(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[setClipboardPolicyRequest](w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	command := &weavecommand.ClipboardCommand{Args: request.toArgs(name)}
	if err := command.Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"updated": name})
}

func (s *APIServer) handleSuspendVM(w http.ResponseWriter, r *http.Request) {
	if !weaveplatform.MacOSAtLeast(14) {
		writeJSON(
			w,
			http.StatusBadRequest,
			errorResponse{Error: "suspend is only available on macOS 14 (Sonoma) or newer"},
		)
		return
	}
	name := chi.URLParam(r, "name")
	command := &weavecommand.SuspendCommand{Name: name}
	if err := command.Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"suspended": name})
}

func (s *APIServer) handleRenameVM(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[renameRequest](w, r)
	if !ok {
		return
	}
	if request.NewName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "newName is required"})
		return
	}
	command := &weavecommand.RenameCommand{Name: chi.URLParam(r, "name"), NewName: request.NewName}
	if err := command.Validate(); err != nil {
		writeError(w, err)
		return
	}
	if err := command.Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"renamed": request.NewName})
}

// toArgs translates the full run request into the "weave run" CLI flags that
// the detached subprocess re-parses. Graphics is caller-controlled: a server
// in a GUI login session can open a window, so headless is only the default
// when neither Graphics nor a VNC mode is requested.
func (req runVMRequest) toArgs() []string {
	args := []string{}
	add := func(cond bool, flag string) {
		if cond {
			args = append(args, flag)
		}
	}
	addValue := func(value, flag string) {
		if value != "" {
			args = append(args, flag, value)
		}
	}
	addEach := func(values []string, flag string) {
		for _, value := range values {
			args = append(args, flag, value)
		}
	}

	switch {
	case req.Graphics:
		args = append(args, "--graphics")
	case req.NoGraphics || (!req.VNC && !req.VNCExperimental):
		args = append(args, "--no-graphics")
	}

	add(req.VNC, "--vnc")
	add(req.VNCExperimental, "--vnc-experimental")
	addValue(req.VNCPassword, "--vnc-password")

	add(req.Serial, "--serial")
	addValue(req.SerialPath, "--serial-path")

	add(req.NoAudio, "--no-audio")

	add(req.Clipboard, "--clipboard")
	add(req.NoClipboard, "--no-clipboard")
	addValue(req.ClipboardUser, "--clipboard-user")
	addValue(req.ClipboardPassword, "--clipboard-password")
	addValue(req.ClipboardDirection, "--clipboard-direction")
	addValue(req.ClipboardFormats, "--clipboard-formats")
	addValue(req.ClipboardFiles, "--clipboard-files")
	addValue(req.ClipboardAllowedTypes, "--clipboard-allowed-types")
	if req.ClipboardSessionMbps != 0 {
		args = append(args, "--clipboard-session-mbps", strconv.Itoa(req.ClipboardSessionMbps))
	}
	if req.ClipboardBandwidthPct != 0 {
		args = append(args, "--clipboard-bandwidth-pct", strconv.Itoa(req.ClipboardBandwidthPct))
	}
	if req.ClipboardMaxBytes != 0 {
		args = append(args, "--clipboard-max-bytes", strconv.FormatInt(req.ClipboardMaxBytes, 10))
	}

	addEach(req.Disks, "--disk")
	addEach(req.Dirs, "--dir")
	addEach(req.SharedDirs, "--shared-dir")
	addEach(req.Mounts, "--mount")
	addEach(req.USBStorage, "--usb-storage")

	addValue(req.NetProfile, "--net-profile")
	addEach(req.NetBridged, "--net-bridged")
	addEach(req.NetDevice, "--net-device")
	add(req.NetHost, "--net-host")
	add(req.NetSoftnet, "--net-softnet")
	addValue(req.NetSoftnetAllow, "--net-softnet-allow")
	addValue(req.NetSoftnetBlock, "--net-softnet-block")
	addValue(req.NetSoftnetExpose, "--net-softnet-expose")

	addValue(req.Rosetta, "--rosetta")
	add(req.Nested, "--nested")
	add(req.Recovery, "--recovery")
	add(req.Suspendable, "--suspendable")
	addValue(req.RootDiskOpts, "--root-disk-opts")

	add(req.CaptureSystemKeys, "--capture-system-keys")
	add(req.NoTrackpad, "--no-trackpad")
	add(req.NoPointer, "--no-pointer")
	add(req.NoKeyboard, "--no-keyboard")

	return args
}
