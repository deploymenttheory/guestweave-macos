// Hand-written helper (the rest of this package is generated): a transport-
// agnostic pump for the Agent Exec bidirectional stream. Both the CLI "exec"
// command and the HTTP API's exec endpoints drive it, supplying their own
// io streams (local terminal, or a WebSocket) and resize source.
//go:build darwin

package agentrpc

import (
	"context"
	"fmt"
	"io"
	"sync"

	"google.golang.org/grpc"
)

// TerminalDimensions is a (cols, rows) pair for PTY sizing.
type TerminalDimensions struct {
	Cols uint32
	Rows uint32
}

// ExecStreamOptions configures a single RunExecStream invocation.
type ExecStreamOptions struct {
	Command     []string
	Interactive bool
	TTY         bool
	Stdin       io.Reader // streamed to the guest when Interactive is set
	Stdout      io.Writer
	Stderr      io.Writer
	InitialSize TerminalDimensions        // initial PTY size when TTY is set
	Resize      <-chan TerminalDimensions // optional; forwarded as resize events
}

// RunExecStream opens an Exec stream on conn, runs Command, and pumps IO
// according to opts until the guest reports an exit event, the stream fails,
// or ctx is cancelled. It returns the guest's exit code (0 when the stream
// ends without an explicit exit event).
func RunExecStream(
	ctx context.Context,
	conn grpc.ClientConnInterface,
	opts ExecStreamOptions,
) (int32, error) {
	execCall, err := NewAgentClient(conn).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("opening exec stream: %w", err)
	}

	command := &ExecRequest_Command{
		Name:        opts.Command[0],
		Args:        opts.Command[1:],
		Interactive: opts.Interactive,
		Tty:         opts.TTY,
	}
	if opts.TTY && (opts.InitialSize.Cols != 0 || opts.InitialSize.Rows != 0) {
		command.TerminalSize = &TerminalSize{
			Cols: opts.InitialSize.Cols,
			Rows: opts.InitialSize.Rows,
		}
	}

	var sendMu sync.Mutex
	send := func(request *ExecRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return execCall.Send(request)
	}

	if err := send(&ExecRequest{Type: &ExecRequest_Command_{Command: command}}); err != nil {
		return 0, err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type exitResult struct {
		code int32
		err  error
	}
	done := make(chan exitResult, 3)

	// Stream the caller's standard input when interactive.
	if opts.Interactive && opts.Stdin != nil {
		go func() {
			buffer := make([]byte, 64*1024)
			for {
				n, readErr := opts.Stdin.Read(buffer)
				if n > 0 {
					if err := send(&ExecRequest{Type: &ExecRequest_StandardInput{
						StandardInput: &IOChunk{Data: append([]byte(nil), buffer[:n]...)},
					}}); err != nil {
						done <- exitResult{err: err}
						return
					}
				}
				if readErr != nil {
					// Signal EOF to the guest.
					_ = send(&ExecRequest{Type: &ExecRequest_StandardInput{
						StandardInput: &IOChunk{Data: nil},
					}})
					if readErr != io.EOF {
						done <- exitResult{err: readErr}
					}
					return
				}
			}
		}()
	}

	// Forward terminal resize events.
	if opts.TTY && opts.Resize != nil {
		go func() {
			for {
				select {
				case size, ok := <-opts.Resize:
					if !ok {
						return
					}
					if err := send(&ExecRequest{Type: &ExecRequest_TerminalResize{
						TerminalResize: &TerminalSize{Cols: size.Cols, Rows: size.Rows},
					}}); err != nil {
						done <- exitResult{err: err}
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Process command events.
	go func() {
		for {
			response, err := execCall.Recv()
			if err != nil {
				done <- exitResult{err: err}
				return
			}
			switch event := response.GetType().(type) {
			case *ExecResponse_StandardOutput:
				if opts.Stdout != nil {
					_, _ = opts.Stdout.Write(event.StandardOutput.GetData())
				}
			case *ExecResponse_StandardError:
				if opts.Stderr != nil {
					_, _ = opts.Stderr.Write(event.StandardError.GetData())
				}
			case *ExecResponse_Exit_:
				done <- exitResult{code: event.Exit.GetCode()}
				return
			}
		}
	}()

	select {
	case result := <-done:
		return result.code, result.err
	case <-ctx.Done():
		return 0, fmt.Errorf("exec stream cancelled: %w", ctx.Err())
	}
}
