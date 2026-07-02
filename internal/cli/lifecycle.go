// Per-command lifecycle: telemetry span + pre-command GC + file logging
// around every verb, error → exit-code mapping, and the macOS main-thread
// split. Ported from the pre-cobra execute.go (itself a port of tart's
// Root.swift main()).
//
// The "run" command owns the main thread (it drives an AppKit run loop);
// every other command runs on a background goroutine while the main thread
// pumps the main dispatch queue, so RunOnMainThread keeps working.
//go:build darwin

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"
	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/logging"
	"github.com/deploymenttheory/guestweave/internal/objcutil"
	"github.com/deploymenttheory/guestweave/internal/telemetry"

	mainthread "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/tools/grandcentraldispatch/mainthread"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// commandName reports the root-child verb for spans, GC policy and logging.
// Subtree leaves keep their parent's name ("snapshot create" → "snapshot"),
// matching the previous dispatcher's span names.
func commandName(cmd *cobra.Command) string {
	for cmd.HasParent() && cmd.Parent().HasParent() {
		cmd = cmd.Parent()
	}
	return cmd.Name()
}

// commandLifecycle carries the telemetry state opened by beginCommand.
type commandLifecycle struct {
	name  string
	span  trace.Span
	start time.Time
}

// beginCommand opens the command span and runs the pre-command chores.
// Called after flag/args validation so parse errors stay telemetry-free.
func beginCommand(name string) *commandLifecycle {
	telemetry.Configure()
	lc := &commandLifecycle{name: name, span: startCommandSpan(name), start: time.Now()}
	runGarbageCollection(name)

	// Logging the logs command itself would mutate the files being read.
	if name != "logs" {
		logging.LogInfo("command %q started: %v", name, os.Args[1:])
	}
	return lc
}

// finish is the single exit path for every executed command: it records the
// outcome, flushes telemetry and terminates the process. It never returns.
func (lc *commandLifecycle) finish(err error) {
	if err == nil {
		recordCommandDuration(context.Background(), lc.name, lc.start)
		lc.span.End()
		telemetry.OTelShared().Flush()
		os.Exit(0)
	}

	// Not an error, just a custom exit code from "weave exec".
	var execExitCode *weaveerrors.ExecCustomExitCodeError
	if errors.As(err, &execExitCode) {
		recordCommandDuration(context.Background(), lc.name, lc.start)
		lc.span.End()
		telemetry.OTelShared().Flush()
		os.Exit(int(execExitCode.Code))
	}

	lc.span.RecordError(err)
	recordCommandDuration(context.Background(), lc.name, lc.start)
	lc.span.End()

	logging.LogError("%v", err)
	fmt.Fprintln(os.Stderr, err)

	var withExitCode weaveerrors.HasExitCode
	if errors.As(err, &withExitCode) {
		telemetry.OTelShared().Flush()
		os.Exit(int(withExitCode.ExitCode()))
	}

	telemetry.OTelShared().Flush()
	os.Exit(1)
}

// runBackground executes work for every command except `run`. The work runs
// on a goroutine (cancelled by SIGINT) while the locked main thread parks in
// DispatchMain so the main dispatch queue stays serviced — the idiomatic
// bindings auto-dispatch @MainActor calls there. The goroutine ends the
// process via finish; DispatchMain never returns.
func runBackground(cmd *cobra.Command, work func(ctx context.Context) error) error {
	lc := beginCommand(commandName(cmd))

	ctx, cancel := context.WithCancel(context.Background())
	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, syscall.SIGINT)
	go func() {
		<-sigint
		cancel()
	}()

	go func() {
		lc.finish(work(ctx))
	}()

	mainthread.DispatchMain()
	return nil // unreachable
}

// runMainThread executes work for the `run` command only: inline on the
// locked main thread, letting it own the AppKit run loop at the top level.
// The workflow either returns a pre-flight error or ends the process from
// inside (NSApplication.run() / os.Exit); finish never returns.
func runMainThread(cmd *cobra.Command, work func() error) error {
	lc := beginCommand(commandName(cmd))
	lc.finish(work())
	return nil // unreachable
}

// startCommandSpan ports Root.startCommandSpan(for:).
func startCommandSpan(commandName string) trace.Span {
	_, span := telemetry.OTelShared().Tracer.Start(context.Background(), commandName)

	// Enrich the root command span with the command's arguments.
	span.SetAttributes(attribute.StringSlice("Command-line arguments", os.Args))

	// Enrich the root command span with Cirrus CI-specific tags.
	if tags, ok := objcutil.EnvironmentValue("CIRRUS_SENTRY_TAGS"); ok {
		for tag := range strings.SplitSeq(tags, ",") {
			if key, value, ok := strings.Cut(tag, "="); ok {
				span.SetAttributes(attribute.String(key, value))
			}
		}
	}

	return span
}

// runGarbageCollection ports Root.runGarbageCollection(for:): run GC before
// each command except pull and clone (it shouldn't take too long).
func runGarbageCollection(commandName string) {
	if commandName == "pull" || commandName == "clone" {
		return
	}
	config, err := weaveconfig.NewConfig()
	if err == nil {
		err = config.GC()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to perform garbage collection: %v\n", err)
	}
}

// recordCommandDuration records the wall-clock duration of the given command
// to the weave.command.duration_ms metric instrument.
func recordCommandDuration(ctx context.Context, commandName string, start time.Time) {
	ms := time.Since(start).Milliseconds()
	telemetry.OTelShared().Instruments.CommandDuration.Record(ctx, ms,
		metric.WithAttributes(attribute.String("command", commandName)))
}
