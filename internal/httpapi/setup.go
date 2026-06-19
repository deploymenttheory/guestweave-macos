// Unattended Setup Assistant automation. Preset mode runs synchronously (it
// finishes in a bounded time); agent mode is long-running and Claude-driven,
// so it runs as an async job polled at GET /weave/pull/{id}.
//go:build darwin

package httpapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	weavecommand "github.com/deploymenttheory/weave/internal/command"
)

func (s *APIServer) handleSetupVM(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[setupRequest](w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")

	mode := request.Mode
	if mode == "" {
		mode = "preset"
	}
	command := &weavecommand.SetupCommand{
		Name:          name,
		Mode:          mode,
		Unattended:    request.Unattended,
		AnthropicKey:  request.AnthropicKey,
		Model:         request.Model,
		MaxIterations: request.MaxIterations,
		SystemPrompt:  request.SystemPrompt,
		ShowScreen:    request.ShowScreen,
	}
	if command.Model == "" {
		command.Model = "claude-sonnet-4-6"
	}
	if command.MaxIterations == 0 {
		command.MaxIterations = 200
	}
	if err := command.Validate(); err != nil {
		writeError(w, err)
		return
	}

	// Agent mode boots the VM and drives a long computer-use loop; run it as a
	// background job and return a pollable id.
	if mode == "agent" {
		id := s.pull.start("setup:"+name, func() error {
			return command.Run(context.Background())
		})
		writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
		return
	}

	// Preset mode runs synchronously (lume's handler behaves the same way).
	if err := command.Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"setup": name})
}
