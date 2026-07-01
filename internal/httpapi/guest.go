// Guest-interaction handlers: command execution over SSH (exec uses default
// credentials, ssh takes them), plus IP resolution and fully-qualified name
// lookup. The interactive WebSocket variants live in ws.go.
//go:build darwin

package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/deploymenttheory/guestweave/internal/macaddress"
	"github.com/deploymenttheory/guestweave/internal/oci"
	weavessh "github.com/deploymenttheory/guestweave/internal/ssh"
	"github.com/deploymenttheory/guestweave/internal/vmservice"
	"github.com/deploymenttheory/guestweave/internal/vmstorage"
)

// handleExec runs a command in the guest over SSH (combined output + exit
// code) using the default weave/weave credentials. Unlike ssh it takes no
// credentials, mirroring the convenience of the "weave exec" command.
func (s *APIServer) handleExec(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[execRequest](w, r)
	if !ok {
		return
	}
	if len(request.Command) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "command is required"})
		return
	}
	resolver, ok := parseResolver(w, request.Resolver)
	if !ok {
		return
	}
	ip, found, err := vmservice.ResolveVMIP(
		r.Context(),
		chi.URLParam(r, "name"),
		resolver,
		request.Wait,
	)
	if err != nil {
		writeError(w, err)
		return
	}
	if !found {
		writeJSON(
			w,
			http.StatusNotFound,
			errorResponse{Error: "no IP address found, is the VM running?"},
		)
		return
	}

	// A zero timeout means no timeout: exec commands may be long-running.
	result, err := sshExecuteWithFallback(
		r.Context(),
		ip,
		"weave",
		"weave",
		0,
		strings.Join(request.Command, " "),
	)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, execResponse{ExitCode: result.ExitCode, Output: result.Output})
}

func (s *APIServer) handleSSH(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[sshRequest](w, r)
	if !ok {
		return
	}
	if len(request.Command) == 0 {
		writeJSON(
			w,
			http.StatusBadRequest,
			errorResponse{
				Error: "command is required (use the ssh/ws endpoint for an interactive shell)",
			},
		)
		return
	}
	resolver, ok := parseResolver(w, request.Resolver)
	if !ok {
		return
	}
	ip, found, err := vmservice.ResolveVMIP(
		r.Context(),
		chi.URLParam(r, "name"),
		resolver,
		request.Wait,
	)
	if err != nil {
		writeError(w, err)
		return
	}
	if !found {
		writeJSON(
			w,
			http.StatusNotFound,
			errorResponse{Error: "no IP address found, is the VM running?"},
		)
		return
	}

	user, password := request.User, request.Password
	if user == "" {
		user = "weave"
	}
	if password == "" {
		password = "weave"
	}
	timeout := request.Timeout
	if timeout == 0 {
		timeout = 60
	}

	result, err := sshExecuteWithFallback(
		r.Context(),
		ip,
		user,
		password,
		time.Duration(timeout)*time.Second,
		strings.Join(request.Command, " "),
	)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sshResponse{ExitCode: result.ExitCode, Output: result.Output})
}

func (s *APIServer) handleIP(w http.ResponseWriter, r *http.Request) {
	resolver, ok := parseResolver(w, r.URL.Query().Get("resolver"))
	if !ok {
		return
	}
	var wait uint16
	if raw := r.URL.Query().Get("wait"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 16)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid wait parameter"})
			return
		}
		wait = uint16(parsed)
	}
	ip, found, err := vmservice.ResolveVMIP(r.Context(), chi.URLParam(r, "name"), resolver, wait)
	if err != nil {
		writeError(w, err)
		return
	}
	if !found {
		writeJSON(
			w,
			http.StatusNotFound,
			errorResponse{Error: "no IP address found, is the VM running?"},
		)
		return
	}
	writeJSON(w, http.StatusOK, ipResponse{IP: ip})
}

func (s *APIServer) handleFQN(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	remoteName, err := oci.NewRemoteName(name)
	if err != nil {
		// Not an OCI reference: echo the local name back.
		writeJSON(w, http.StatusOK, fqnResponse{FQN: name})
		return
	}
	storage, err := vmstorage.NewVMStorageOCI()
	if err != nil {
		writeError(w, err)
		return
	}
	digest, err := storage.Digest(remoteName)
	if err != nil {
		writeError(w, err)
		return
	}
	remoteName.Reference = oci.NewDigestReference(digest)
	writeJSON(w, http.StatusOK, fqnResponse{FQN: remoteName.String()})
}

// parseResolver resolves the IP-resolution strategy name, defaulting to DHCP.
// It writes a 400 and returns ok=false on an unknown value.
func parseResolver(w http.ResponseWriter, raw string) (macaddress.IPResolutionStrategy, bool) {
	if raw == "" {
		return macaddress.IPResolutionStrategyDHCP, true
	}
	strategy, ok := macaddress.ParseIPResolutionStrategy(raw)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "unsupported resolver: " + raw})
		return macaddress.IPResolutionStrategyDHCP, false
	}
	return strategy, true
}

// sshExecuteWithFallback runs a command with the in-process SSH client,
// falling back to the system ssh binary on a connection failure (mirroring
// the CLI ssh command).
func sshExecuteWithFallback(
	ctx context.Context,
	ip, user, password string,
	timeout time.Duration,
	command string,
) (weavessh.SSHResult, error) {
	result, err := weavessh.NewSSHClient(ip, 22, user, password).Execute(ctx, command, timeout)
	var sshErr *weavessh.SSHError
	if errors.As(err, &sshErr) && sshErr.Kind == weavessh.SSHErrorConnectionFailed {
		result, err = weavessh.NewSystemSSHClient(ip, 22, user, password).
			Execute(ctx, command, timeout)
	}
	if err != nil {
		return result, fmt.Errorf("ssh exec: %w", err)
	}
	return result, nil
}
