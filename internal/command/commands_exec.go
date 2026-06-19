// Port of tart's Commands/Exec.swift: executes a command in a running VM
// through the Tart Guest Agent's Exec streaming gRPC. The stream pump itself
// lives in internal/agentrpc so the HTTP API can reuse it; this command wires
// it to the local terminal.
//go:build darwin

package command

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/deploymenttheory/weave/internal/agentrpc"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
	"github.com/deploymenttheory/weave/internal/terminal"
	"github.com/deploymenttheory/weave/internal/vmstorage"
)

// ExecCommand ports the Exec command.
type ExecCommand struct {
	Interactive bool
	TTY         bool
	Name        string
	Command     []string
}

func (c *ExecCommand) Run(ctx context.Context) error {
	if !weaveplatform.MacOSAtLeast(14) {
		return weaveerrors.ErrGeneric(
			"\"weave exec\" is only available on macOS 14 (Sonoma) or newer",
		)
	}

	// Open the VM's directory and ensure that the VM is running.
	storage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}
	vmDir, err := storage.Open(c.Name)
	if err != nil {
		return err
	}
	running, err := vmDir.Running()
	if err != nil {
		return err
	}
	if !running {
		return weaveerrors.ErrVMNotRunning(c.Name)
	}

	// Change the current working directory to the VM's base directory to
	// work around the 104-byte Unix domain socket path limitation.
	controlSocketPath := vmDir.ControlSocketURL()
	if dir := filepath.Dir(controlSocketPath); dir != "" {
		_ = os.Chdir(dir)
	}

	conn, err := grpc.NewClient("unix://"+filepath.Base(controlSocketPath),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return weaveerrors.ErrGeneric(
			"Failed to connect to the VM using its control socket: %v, is the Tart Guest Agent running?",
			err,
		)
	}
	defer conn.Close()

	// Switch the controlling terminal into raw mode when a remote
	// pseudo-terminal is requested.
	var state *terminal.TermState
	if c.TTY && terminal.TermIsTerminal() {
		state, err = terminal.TermMakeRaw()
		if err != nil {
			return err
		}
	}
	defer func() {
		// Restore the terminal to its initial state.
		if state != nil {
			_ = terminal.TermRestore(state)
		}
	}()

	return c.execute(ctx, conn)
}

func (c *ExecCommand) execute(ctx context.Context, conn *grpc.ClientConn) error {
	opts := agentrpc.ExecStreamOptions{
		Command:     c.Command,
		Interactive: c.Interactive,
		TTY:         c.TTY,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
	}
	if c.Interactive {
		opts.Stdin = os.Stdin
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Forward the host terminal's initial size and subsequent resizes.
	if c.TTY {
		if width, height, err := terminal.TermGetSize(); err == nil {
			opts.InitialSize = agentrpc.TerminalDimensions{
				Cols: uint32(width),
				Rows: uint32(height),
			}
		}
		resize := make(chan agentrpc.TerminalDimensions, 1)
		opts.Resize = resize

		sigwinch := make(chan os.Signal, 1)
		signal.Notify(sigwinch, syscall.SIGWINCH)
		defer signal.Stop(sigwinch)
		go func() {
			for {
				select {
				case <-sigwinch:
					if width, height, err := terminal.TermGetSize(); err == nil {
						select {
						case resize <- agentrpc.TerminalDimensions{Cols: uint32(width), Rows: uint32(height)}:
						case <-ctx.Done():
							return
						}
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	code, err := agentrpc.RunExecStream(ctx, conn, opts)
	if err != nil {
		return err
	}
	// Preserve the guest's exit code as the process exit status (handleError
	// maps ExecCustomExitCodeError to os.Exit).
	return &weaveerrors.ExecCustomExitCodeError{Code: code}
}
