// Package clipboardctl is the host-side control channel for live clipboard
// policy updates. A running `weave run` process listens on a per-VM Unix domain
// socket; `weave clipboard set` (and the HTTP API, via that command) dials it to
// push a clipboardpolicy.Override onto the running engine without a restart.
//
// The protocol is a single JSON Request followed by a single JSON Response on
// the same connection (json.Decoder/Encoder), so no framing is needed.
//go:build darwin

package clipboardctl

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"time"

	"github.com/deploymenttheory/weave/internal/clipboardpolicy"
)

// Request is a live clipboard-policy update sent by a client.
type Request struct {
	// Override carries the per-field changes to layer onto the running policy.
	Override clipboardpolicy.Override `json:"override"`
	// Persist also writes the resulting policy to the VM's stored config so it
	// survives a restart.
	Persist bool `json:"persist"`
}

// Response reports the outcome and the resulting effective policy.
type Response struct {
	OK     bool                    `json:"ok"`
	Error  string                  `json:"error,omitempty"`
	Policy *clipboardpolicy.Policy `json:"policy,omitempty"`
}

// Handler applies a request and returns the resulting effective policy. It is
// supplied by the run command and calls Engine.SetPolicy (and optionally
// persists). Returning an error yields an error Response.
type Handler func(Request) (clipboardpolicy.Policy, error)

// Serve listens on socketPath and dispatches each connection to handler until
// ctx is cancelled. The socket file is created (replacing any stale one) and
// removed on exit.
func Serve(ctx context.Context, socketPath string, handler Handler) error {
	_ = os.Remove(socketPath)
	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "unix", socketPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go handleConn(conn, handler)
	}
}

func handleConn(conn net.Conn, handler Handler) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(Response{Error: "decode request: " + err.Error()})
		return
	}

	policy, err := handler(req)
	if err != nil {
		_ = json.NewEncoder(conn).Encode(Response{Error: err.Error()})
		return
	}
	_ = json.NewEncoder(conn).Encode(Response{OK: true, Policy: &policy})
}

// Send dials socketPath, sends req, and returns the response. It is the client
// half used by `weave clipboard set`.
func Send(ctx context.Context, socketPath string, req Request) (Response, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}
