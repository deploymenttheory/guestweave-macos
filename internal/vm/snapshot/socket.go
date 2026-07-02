// Snapshot command socket: a tiny per-VM Unix socket served by the "weave run"
// process so external clients (`weave snapshot create`, the REST API) can
// snapshot a *running* VM. Only the run process owns the VZ handle, so it
// performs the pause → clone → resume itself (via LiveHandler). Stopped VMs
// are snapshotted directly by the client and never touch this socket.
//go:build darwin

package snapshot

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"time"

	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/vmdirectory"
)

const socketName = "snapshot.sock"

// LiveHandler is implemented by the run process's Session: only it owns the
// VZ handle needed to pause → clone → resume or revert in place.
type LiveHandler interface {
	// CreateLive snapshots the running VM (pause → clone → resume).
	CreateLive(name, description string) (Snapshot, error)
	// RevertInProcess triggers an in-process revert; false means it is
	// unavailable (e.g. VNC runs), and the client is told to stop-and-revert
	// instead.
	RevertInProcess(ref string) bool
}

// socketRequest / socketResponse are the newline-delimited JSON wire format
// spoken on the snapshot socket.
type socketRequest struct {
	Command     string `json:"command"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type socketResponse struct {
	Snapshot *Snapshot `json:"snapshot,omitempty"`
	Error    string    `json:"error,omitempty"`
}

func socketPath(d *vmdirectory.VMDirectory) string {
	return filepath.Join(d.Path(), socketName)
}

// Serve binds the snapshot socket and handles requests until ctx is
// cancelled. Best-effort: if binding fails, running-VM snapshots are simply
// unavailable (the client falls back to reporting the VM is busy).
func Serve(ctx context.Context, d *vmdirectory.VMDirectory, h LiveHandler) {
	path := socketPath(d)
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
		_ = os.Remove(path)
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go handleConn(h, conn)
	}
}

func handleConn(h LiveHandler, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Minute))

	var req socketRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		writeResponse(conn, socketResponse{Error: "invalid request: " + err.Error()})
		return
	}

	switch req.Command {
	case "create":
		snap, err := h.CreateLive(req.Name, req.Description)
		if err != nil {
			writeResponse(conn, socketResponse{Error: err.Error()})
			return
		}
		writeResponse(conn, socketResponse{Snapshot: &snap})
	case "revert":
		// req.Name carries the snapshot ref. Trigger an in-process revert; the
		// run loop rebuilds the VM and re-points the window in place.
		if !h.RevertInProcess(req.Name) {
			writeResponse(conn, socketResponse{
				Error: "in-process revert is unavailable for this run (e.g. VNC); stop the VM and revert instead"})
			return
		}
		writeResponse(conn, socketResponse{})
	default:
		writeResponse(conn, socketResponse{Error: "unknown command: " + req.Command})
	}
}

func writeResponse(conn net.Conn, resp socketResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(data, '\n'))
}

// RequestCreateOverSocket asks the run process to snapshot a running VM. The
// returned error distinguishes "no run process listening" (so the caller can
// report the VM as not running) via ErrSocketUnavailable.
func RequestCreateOverSocket(d *vmdirectory.VMDirectory, name, description string) (Snapshot, error) {
	conn, err := net.DialTimeout("unix", socketPath(d), 5*time.Second)
	if err != nil {
		return Snapshot{}, ErrSocketUnavailable
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Minute))

	req := socketRequest{Command: "create", Name: name, Description: description}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Snapshot{}, err
	}

	var resp socketResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Snapshot{}, weaveerrors.ErrGeneric("the run process did not return a snapshot result: %v", err)
	}
	if resp.Error != "" {
		return Snapshot{}, weaveerrors.ErrGeneric("%s", resp.Error)
	}
	if resp.Snapshot == nil {
		return Snapshot{}, weaveerrors.ErrGeneric("the run process returned an empty snapshot result")
	}
	return *resp.Snapshot, nil
}

// RequestRevertOverSocket asks the run process to revert a running VM in place.
// Returns ErrSocketUnavailable when no run process is listening.
func RequestRevertOverSocket(d *vmdirectory.VMDirectory, ref string) error {
	conn, err := net.DialTimeout("unix", socketPath(d), 5*time.Second)
	if err != nil {
		return ErrSocketUnavailable
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	if err := json.NewEncoder(conn).Encode(socketRequest{Command: "revert", Name: ref}); err != nil {
		return err
	}
	var resp socketResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return weaveerrors.ErrGeneric("the run process did not return a revert result: %v", err)
	}
	if resp.Error != "" {
		return weaveerrors.ErrGeneric("%s", resp.Error)
	}
	return nil
}

// ErrSocketUnavailable means no run process is listening on the VM's
// snapshot socket (the VM is not running, or is too old to serve it).
var ErrSocketUnavailable = weaveerrors.ErrGeneric("no run process is serving the VM")
