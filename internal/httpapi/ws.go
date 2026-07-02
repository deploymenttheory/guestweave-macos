// Interactive exec/ssh over WebSocket, both backed by SSH. Binary frames carry
// stdin/stdout; text frames carry a {"cols":N,"rows":N} resize control
// message. exec runs the command from repeated ?cmd= query parameters under a
// PTY; ssh opens a login shell.
//go:build darwin

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	weavessh "github.com/deploymenttheory/guestweave/internal/ssh"
	vmservice "github.com/deploymenttheory/guestweave/internal/vm/service"
)

// handleExecWS runs the ?cmd= command interactively over SSH (default
// weave/weave credentials), mirroring "weave exec -it".
func (s *APIServer) handleExecWS(w http.ResponseWriter, r *http.Request) {
	command := strings.Join(r.URL.Query()["cmd"], " ")
	if command == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "at least one cmd query parameter is required"})
		return
	}
	s.serveSSHWebSocket(w, r, command, "weave", "weave")
}

// handleSSHWS opens an interactive login shell over SSH.
func (s *APIServer) handleSSHWS(w http.ResponseWriter, r *http.Request) {
	user, password := r.URL.Query().Get("user"), r.URL.Query().Get("password")
	if user == "" {
		user = "weave"
	}
	if password == "" {
		password = "weave"
	}
	s.serveSSHWebSocket(w, r, "", user, password)
}

// serveSSHWebSocket resolves the VM's IP, upgrades to a WebSocket, and bridges
// it to an interactive SSH session running command (empty => login shell).
func (s *APIServer) serveSSHWebSocket(
	w http.ResponseWriter,
	r *http.Request,
	command, user, password string,
) {
	resolver, ok := parseResolver(w, r.URL.Query().Get("resolver"))
	if !ok {
		return
	}
	var wait uint16
	if raw := r.URL.Query().Get("wait"); raw != "" {
		if parsed, err := strconv.ParseUint(raw, 10, 16); err == nil {
			wait = uint16(parsed)
		}
	}
	ip, found, err := vmservice.ResolveVMIP(r.Context(), chi.URLParam(r, "name"), resolver, wait)
	if err != nil {
		writeError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "no IP address found, is the VM running?"})
		return
	}

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := r.Context()

	pr, pw := io.Pipe()
	resize := make(chan [2]uint16, 1)
	go pumpWSInput(ctx, c, pw, func(cols, rows uint32) {
		select {
		case resize <- [2]uint16{uint16(cols), uint16(rows)}:
		default:
		}
	})

	cols, rows := wsInitialSize(r)
	writer := wsBinaryWriter{ctx: ctx, conn: c}
	err = weavessh.NewSSHClient(ip, 22, user, password).
		InteractiveIO(ctx, command, pr, writer, writer, cols, rows, resize)
	closeWS(ctx, c, err)
}

// wsBinaryWriter forwards each Write as a binary WebSocket frame.
type wsBinaryWriter struct {
	ctx  context.Context
	conn *websocket.Conn
}

func (w wsBinaryWriter) Write(p []byte) (int, error) {
	if err := w.conn.Write(w.ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// pumpWSInput reads frames from the WebSocket: binary frames are forwarded to
// stdin, text frames are parsed as {"cols","rows"} resize control messages.
func pumpWSInput(
	ctx context.Context,
	c *websocket.Conn,
	stdin *io.PipeWriter,
	onResize func(cols, rows uint32),
) {
	defer stdin.Close()
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		switch typ {
		case websocket.MessageText:
			var ctrl struct {
				Cols uint32 `json:"cols"`
				Rows uint32 `json:"rows"`
			}
			if json.Unmarshal(data, &ctrl) == nil && onResize != nil {
				onResize(ctrl.Cols, ctrl.Rows)
			}
		case websocket.MessageBinary:
			if _, err := stdin.Write(data); err != nil {
				return
			}
		}
	}
}

// wsInitialSize reads the optional cols/rows query parameters.
func wsInitialSize(r *http.Request) (cols, rows uint16) {
	cols, rows = 80, 24
	if raw := r.URL.Query().Get("cols"); raw != "" {
		if parsed, err := strconv.ParseUint(raw, 10, 16); err == nil {
			cols = uint16(parsed)
		}
	}
	if raw := r.URL.Query().Get("rows"); raw != "" {
		if parsed, err := strconv.ParseUint(raw, 10, 16); err == nil {
			rows = uint16(parsed)
		}
	}
	return cols, rows
}

// closeWS sends a final status frame and closes the WebSocket, reporting any
// non-zero exit code (carried by ExecCustomExitCodeError) or error.
func closeWS(ctx context.Context, c *websocket.Conn, err error) {
	status := websocket.StatusNormalClosure
	reason := ""
	var exitErr *weaveerrors.ExecCustomExitCodeError
	switch {
	case err == nil || errors.Is(err, context.Canceled):
	case errors.As(err, &exitErr):
		reason = "exit code " + strconv.Itoa(int(exitErr.Code))
	default:
		status = websocket.StatusInternalError
		reason = err.Error()
	}
	if payload, mErr := json.Marshal(map[string]any{"error": reason}); mErr == nil {
		writeCtx, cancel := context.WithTimeout(ctx, time.Second)
		_ = c.Write(writeCtx, websocket.MessageText, payload)
		cancel()
	}
	_ = c.Close(status, reason)
}
