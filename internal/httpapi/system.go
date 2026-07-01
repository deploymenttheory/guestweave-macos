// Host- and maintenance-level handlers: prune, log tailing, host status.
//go:build darwin

package httpapi

import (
	"net/http"
	"runtime"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/deploymenttheory/guestweave/internal/ci"
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/deploymenttheory/guestweave/internal/httpapi/schema"
)

// handleOpenAPI serves the embedded OpenAPI document.
func (s *APIServer) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(schema.OpenAPI)
}

func (s *APIServer) handlePrune(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[pruneRequest](w, r)
	if !ok {
		return
	}
	entries := request.Entries
	if entries == "" {
		entries = "caches"
	}
	command := &weavecommand.PruneCommand{
		Entries:     entries,
		OlderThan:   request.OlderThan,
		SpaceBudget: request.SpaceBudget,
		GC:          request.GC,
	}
	if err := command.Validate(); err != nil {
		writeError(w, err)
		return
	}
	if err := command.Run(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"pruned": entries})
}

func (s *APIServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	logType := r.URL.Query().Get("type")
	if logType == "" {
		logType = "all"
	}
	lines := 0
	if rawLines := r.URL.Query().Get("lines"); rawLines != "" {
		parsed, err := strconv.Atoi(rawLines)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid lines parameter"})
			return
		}
		lines = parsed
	}
	command := &weavecommand.LogsCommand{Type: logType, Lines: lines}
	if err := command.Validate(); err != nil {
		writeError(w, err)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, file := range command.LogFiles() {
		_ = weavecommand.WriteTail(w, file.Path, file.Prefix, lines)
	}
}

func (s *APIServer) handleHostStatus(w http.ResponseWriter, r *http.Request) {
	memory, _ := unix.SysctlUint64("hw.memsize")
	model, _ := syscall.Sysctl("hw.model")
	writeJSON(w, http.StatusOK, hostStatusResponse{
		Version:     ci.CIVersion(),
		Model:       model,
		CPUCount:    runtime.NumCPU(),
		MemoryBytes: memory,
	})
}
