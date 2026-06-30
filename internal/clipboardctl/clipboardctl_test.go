//go:build darwin

package clipboardctl

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/deploymenttheory/weave/internal/clipboardpolicy"
)

func TestSendServeRoundTrip(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "clip.sock")

	// Handler applies the override onto a fixed base and echoes the result.
	var gotPersist bool
	handler := func(req Request) (clipboardpolicy.Policy, error) {
		gotPersist = req.Persist
		return req.Override.Apply(clipboardpolicy.Default()), nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Serve(ctx, socket, handler) }()

	// Wait for the socket to be ready.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := Send(context.Background(), socket, Request{}); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server never came up")
		}
		time.Sleep(10 * time.Millisecond)
	}

	dir := clipboardpolicy.DirectionGuestToHost
	resp, err := Send(context.Background(), socket, Request{
		Override: clipboardpolicy.Override{Direction: &dir},
		Persist:  true,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !resp.OK || resp.Policy == nil {
		t.Fatalf("response not ok: %+v", resp)
	}
	if resp.Policy.Direction != clipboardpolicy.DirectionGuestToHost {
		t.Errorf("direction = %s, want guestToHost", resp.Policy.Direction)
	}
	if !gotPersist {
		t.Error("handler did not receive persist=true")
	}
}
