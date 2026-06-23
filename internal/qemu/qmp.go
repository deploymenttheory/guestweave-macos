//go:build darwin

package qemu

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// qmpConn is a minimal QEMU Machine Protocol client over the VM's unix socket.
// It performs the capabilities handshake on dial and exposes execute for the
// handful of commands the backend needs (set_password, system_powerdown).
type qmpConn struct {
	conn net.Conn
	dec  *json.Decoder
	enc  *json.Encoder
}

// dialQMP connects to the QMP socket, reads the greeting and negotiates
// capabilities, retrying until deadline (QEMU creates the socket shortly after
// launch).
func dialQMP(path string, deadline time.Time) (*qmpConn, error) {
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", path, 2*time.Second)
		if err != nil {
			lastErr = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		q := &qmpConn{conn: conn, dec: json.NewDecoder(conn), enc: json.NewEncoder(conn)}
		// Greeting.
		var greeting map[string]json.RawMessage
		if err := q.dec.Decode(&greeting); err != nil {
			conn.Close()
			lastErr = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if err := q.execute("qmp_capabilities", nil); err != nil {
			conn.Close()
			lastErr = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		return q, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out")
	}
	return nil, fmt.Errorf("qmp: connect %s: %w", path, lastErr)
}

// execute sends a QMP command and reads responses until the matching
// return/error (skipping asynchronous events).
func (q *qmpConn) execute(command string, args map[string]any) error {
	req := map[string]any{"execute": command}
	if args != nil {
		req["arguments"] = args
	}
	if err := q.enc.Encode(req); err != nil {
		return fmt.Errorf("qmp: send %s: %w", command, err)
	}
	for {
		var resp struct {
			Return json.RawMessage `json:"return"`
			Error  *struct {
				Class string `json:"class"`
				Desc  string `json:"desc"`
			} `json:"error"`
			Event string `json:"event"`
		}
		if err := q.dec.Decode(&resp); err != nil {
			return fmt.Errorf("qmp: read %s: %w", command, err)
		}
		if resp.Event != "" {
			continue // asynchronous event; keep reading for the reply
		}
		if resp.Error != nil {
			return fmt.Errorf("qmp: %s: %s: %s", command, resp.Error.Class, resp.Error.Desc)
		}
		return nil
	}
}

// setVNCPassword applies the VNC server password.
func (q *qmpConn) setVNCPassword(password string) error {
	return q.execute("set_password", map[string]any{
		"protocol": "vnc",
		"password": password,
	})
}

// powerdown requests an ACPI graceful shutdown of the guest.
func (q *qmpConn) powerdown() error {
	return q.execute("system_powerdown", nil)
}

func (q *qmpConn) close() {
	if q.conn != nil {
		q.conn.Close()
	}
}
