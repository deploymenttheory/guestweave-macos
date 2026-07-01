// Serial transport for the host side of the guest agent. Unlike the SSH stdio
// Client, the agent here is *resident* in the guest session and reachable over a
// virtio serial console: the host owns one end of two pipes bridged into the VM
// (see the run command), and this wraps those ends in the same framed-transport
// shape the clipboard engine drives.
//go:build darwin

package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/deploymenttheory/guestweave/internal/guestweaveagent/agent"
	"github.com/deploymenttheory/guestweave/internal/guestweaveagent/proto"
)

// Conn is the framed transport the clipboard engine drives — satisfied by both
// the SSH-backed *Client and the serial-backed *SerialConn. Lock/Unlock guard a
// multi-frame exchange; Writer/Reader expose the framed streams.
type Conn interface {
	Lock()
	Unlock()
	Writer() *bufio.Writer
	Reader() *bufio.Reader
	Close() error
	HelloInfo() agent.Hello
}

// HelloInfo returns the agent's handshake identity (for the SSH Client).
func (c *Client) HelloInfo() agent.Hello { return c.Hello }

// SerialConn is a connection to a resident guest agent over a virtio serial
// console. The host writes requests to w and reads responses from r (the host
// ends of the two bridge pipes). NewSerial is created only after the installer
// has started the resident agent, so the host can drive the hello handshake
// directly: it writes one hello and reads the reply in the background. A single
// hello (never repeated) keeps the byte stream framed even if the agent is a
// moment from opening the device — the request simply waits in the pipe buffer.
type SerialConn struct {
	r *bufio.Reader
	w *bufio.Writer

	mu sync.Mutex // serialises multi-frame exchanges, like Client.mu

	hello    agent.Hello
	readyCh  chan struct{}
	readyErr error
}

// NewSerial wraps the host ends of the serial bridge and starts the hello
// handshake. r/w persist for the VM's lifetime (reused across in-process VM
// rebuilds), so Close is a no-op.
func NewSerial(r io.Reader, w io.Writer) *SerialConn {
	s := &SerialConn{
		r:       proto.NewBufferedReader(r),
		w:       proto.NewBufferedWriter(w),
		readyCh: make(chan struct{}),
	}
	go s.handshake()
	return s
}

// handshake sends exactly one hello request and reads its response. It runs in
// the background so a not-yet-ready agent doesn't block the engine; the engine
// treats the agent as connected only once Ready reports true.
func (s *SerialConn) handshake() {
	defer close(s.readyCh)
	if err := proto.WriteRequest(s.w, proto.Request{Module: agent.ModuleName, Op: agent.OpHello}); err != nil {
		s.readyErr = err
		return
	}
	resp, err := proto.ReadResponse(s.r)
	if err != nil {
		s.readyErr = err
		return
	}
	if resp.Err != "" {
		s.readyErr = fmt.Errorf("%s", resp.Err)
		return
	}
	if len(resp.Meta) > 0 {
		_ = json.Unmarshal(resp.Meta, &s.hello)
	}
}

// Ready reports whether the hello handshake has completed (the channel is live
// and frame-synchronised).
func (s *SerialConn) Ready() bool {
	select {
	case <-s.readyCh:
		return s.readyErr == nil
	default:
		return false
	}
}

func (s *SerialConn) Lock()                 { s.mu.Lock() }
func (s *SerialConn) Unlock()               { s.mu.Unlock() }
func (s *SerialConn) Writer() *bufio.Writer { return s.w }
func (s *SerialConn) Reader() *bufio.Reader { return s.r }

// Close is a no-op: the underlying pipes are owned by the run command and reused
// across VM rebuilds.
func (s *SerialConn) Close() error { return nil }

// HelloInfo returns the agent's handshake identity from the banner.
func (s *SerialConn) HelloInfo() agent.Hello { return s.hello }
