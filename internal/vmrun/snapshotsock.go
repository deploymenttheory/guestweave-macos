// Snapshot command socket: a tiny per-VM Unix socket served by the "weave run"
// process so external clients (`weave snapshot create`, the REST API) can
// snapshot a *running* VM. Only the run process owns the VZ handle, so it
// performs the pause → clone → resume itself. Stopped VMs are snapshotted
// directly by the client and never touch this socket.
//go:build darwin

package vmrun

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

const snapshotSocketName = "snapshot.sock"

// snapshotSocketRequest / snapshotSocketResponse are the newline-delimited JSON
// wire format spoken on the snapshot socket.
type snapshotSocketRequest struct {
	Command     string `json:"command"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type snapshotSocketResponse struct {
	Snapshot *vmdirectory.Snapshot `json:"snapshot,omitempty"`
	Error    string                `json:"error,omitempty"`
}

func snapshotSocketPath(vmDir *vmdirectory.VMDirectory) string {
	return filepath.Join(vmDir.Path(), snapshotSocketName)
}

// serveSnapshotSocket binds the snapshot socket and handles requests until ctx
// is cancelled. Best-effort: if binding fails, running-VM snapshots are simply
// unavailable (the client falls back to reporting the VM is busy).
func (c *Session) serveSnapshotSocket(ctx context.Context, vmDir *vmdirectory.VMDirectory) {
	path := snapshotSocketPath(vmDir)
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
		go c.handleSnapshotConn(vmDir, conn)
	}
}

func (c *Session) handleSnapshotConn(vmDir *vmdirectory.VMDirectory, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Minute))

	var req snapshotSocketRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		writeSnapshotResponse(conn, snapshotSocketResponse{Error: "invalid request: " + err.Error()})
		return
	}

	switch req.Command {
	case "create":
		if c.vm == nil {
			writeSnapshotResponse(conn, snapshotSocketResponse{Error: "the VM is not running"})
			return
		}
		snap, err := c.vm.CreateSnapshotPaused(vmDir, req.Name, req.Description)
		if err != nil {
			writeSnapshotResponse(conn, snapshotSocketResponse{Error: err.Error()})
			return
		}
		writeSnapshotResponse(conn, snapshotSocketResponse{Snapshot: &snap})
	case "revert":
		// req.Name carries the snapshot ref. Trigger an in-process revert; the
		// run loop rebuilds the VM and re-points the window in place.
		if !c.TriggerRevert(req.Name) {
			writeSnapshotResponse(conn, snapshotSocketResponse{
				Error: "in-process revert is unavailable for this run (e.g. VNC); stop the VM and revert instead"})
			return
		}
		writeSnapshotResponse(conn, snapshotSocketResponse{})
	default:
		writeSnapshotResponse(conn, snapshotSocketResponse{Error: "unknown command: " + req.Command})
	}
}

func writeSnapshotResponse(conn net.Conn, resp snapshotSocketResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(data, '\n'))
}

// requestSnapshotOverSocket asks the run process to snapshot a running VM. The
// returned error distinguishes "no run process listening" (so the caller can
// report the VM as not running) via ErrSnapshotSocketUnavailable.
func RequestSnapshotOverSocket(vmDir *vmdirectory.VMDirectory, name, description string) (vmdirectory.Snapshot, error) {
	conn, err := net.DialTimeout("unix", snapshotSocketPath(vmDir), 5*time.Second)
	if err != nil {
		return vmdirectory.Snapshot{}, ErrSnapshotSocketUnavailable
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Minute))

	req := snapshotSocketRequest{Command: "create", Name: name, Description: description}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return vmdirectory.Snapshot{}, err
	}

	var resp snapshotSocketResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return vmdirectory.Snapshot{}, weaveerrors.ErrGeneric("the run process did not return a snapshot result: %v", err)
	}
	if resp.Error != "" {
		return vmdirectory.Snapshot{}, weaveerrors.ErrGeneric("%s", resp.Error)
	}
	if resp.Snapshot == nil {
		return vmdirectory.Snapshot{}, weaveerrors.ErrGeneric("the run process returned an empty snapshot result")
	}
	return *resp.Snapshot, nil
}

// requestRevertOverSocket asks the run process to revert a running VM in place.
// Returns errSnapshotSocketUnavailable when no run process is listening.
func RequestRevertOverSocket(vmDir *vmdirectory.VMDirectory, ref string) error {
	conn, err := net.DialTimeout("unix", snapshotSocketPath(vmDir), 5*time.Second)
	if err != nil {
		return ErrSnapshotSocketUnavailable
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	if err := json.NewEncoder(conn).Encode(snapshotSocketRequest{Command: "revert", Name: ref}); err != nil {
		return err
	}
	var resp snapshotSocketResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return weaveerrors.ErrGeneric("the run process did not return a revert result: %v", err)
	}
	if resp.Error != "" {
		return weaveerrors.ErrGeneric("%s", resp.Error)
	}
	return nil
}

// errSnapshotSocketUnavailable means no run process is listening on the VM's
// snapshot socket (the VM is not running, or is too old to serve it).
var ErrSnapshotSocketUnavailable = weaveerrors.ErrGeneric("no run process is serving the VM")
