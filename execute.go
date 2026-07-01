// Port of tart's Root.swift main() entry point: parses the subcommand,
// runs pre-command garbage collection, wraps execution in an OTel span and
// maps errors to exit codes. The "run" command owns the main thread (it
// drives an AppKit run loop); every other command runs on a background
// goroutine while the main thread pumps the main dispatch queue, so
// RunOnMainThread keeps working.
//go:build darwin

package main

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
	"github.com/deploymenttheory/guestweave/internal/terminal"

	mainthread "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/tools/grandcentraldispatch/mainthread"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Main is the weave entry point, invoked from the main package with the main
// goroutine locked to the process's main thread (AppKit and
// Virtualization.framework dispatch their work to the main queue).
func run() {
	// Ensure the default SIGINT handler is disabled; cancellation by Ctrl+C
	// is handled explicitly (Root.main does signal(SIGINT, SIG_IGN)).
	signal.Ignore(syscall.SIGINT)

	// Show the banner and usage on no-args or a help request.
	if args := os.Args[1:]; len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		terminal.PrintBannerWithUsage(os.Stdout, rootSubcommands)
		return
	}

	name, command, err := parseRootCommand(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if command == nil {
		// Informational invocations like --version.
		return
	}

	telemetry.Configure()
	span := startCommandSpan(name)
	commandStart := time.Now()
	runGarbageCollection(name)

	// Logging the logs command itself would mutate the files being read.
	if name != "logs" {
		logging.LogInfo("command %q started: %v", name, os.Args[1:])
	}

	if adapter, isMainThreadCommand := command.(runMainThreadAdapter); isMainThreadCommand {
		// This command drives a run loop on the main thread, so run it right
		// here, letting it own the main thread at the top level.
		if err := adapter.command.RunMainThread(); err != nil {
			handleError(err, span, commandStart, name)
		}
		recordCommandDuration(context.Background(), name, commandStart)
		span.End()
		telemetry.OTelShared().Flush()
		os.Exit(0)
	}

	// Every other command doesn't own the main thread: drive it on a goroutine
	// and hand the main thread to libdispatch so the main queue stays serviced
	// (the idiomatic layer auto-dispatches @MainActor calls there). VM work no
	// longer needs the main queue — VZVirtualMachine runs on its own serial queue
	// — so this is purely to keep any stray main-queue work flowing. The command
	// goroutine ends the process via os.Exit; DispatchMain never returns.
	ctx, cancel := context.WithCancel(context.Background())

	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, syscall.SIGINT)
	go func() {
		<-sigint
		cancel()
	}()

	go func() {
		err := command.Run(ctx)
		if err != nil {
			handleError(err, span, commandStart, name) // os.Exits for handled errors
		}
		recordCommandDuration(ctx, name, commandStart)
		span.End()
		telemetry.OTelShared().Flush()
		os.Exit(0)
	}()

	mainthread.DispatchMain()
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

// handleError ports Root.handleError(_:span:).
func handleError(err error, span trace.Span, commandStart time.Time, commandName string) {
	// Not an error, just a custom exit code from "weave exec".
	var execExitCode *weaveerrors.ExecCustomExitCodeError
	if errors.As(err, &execExitCode) {
		recordCommandDuration(context.Background(), commandName, commandStart)
		span.End()
		telemetry.OTelShared().Flush()
		os.Exit(int(execExitCode.Code))
	}

	// Capture the error into OpenTelemetry.
	span.RecordError(err)
	recordCommandDuration(context.Background(), commandName, commandStart)
	span.End()

	logging.LogError("%v", err)
	fmt.Fprintln(os.Stderr, err)

	// Handle errors that require a specific exit code to be set.
	var withExitCode weaveerrors.HasExitCode
	if errors.As(err, &withExitCode) {
		telemetry.OTelShared().Flush()
		os.Exit(int(withExitCode.ExitCode()))
	}

	telemetry.OTelShared().Flush()
	os.Exit(1)
}

// recordCommandDuration records the wall-clock duration of the given command
// to the weave.command.duration_ms metric instrument.
func recordCommandDuration(ctx context.Context, commandName string, start time.Time) {
	ms := time.Since(start).Milliseconds()
	telemetry.OTelShared().Instruments.CommandDuration.Record(ctx, ms,
		metric.WithAttributes(attribute.String("command", commandName)))
}
