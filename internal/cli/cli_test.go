//go:build darwin

package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestTreeRegisters guards against pflag registration panics (duplicate
// shorthands panic at AddCommand/flag-definition time).
func TestTreeRegisters(t *testing.T) {
	root := NewRootCommand()
	if !root.HasSubCommands() {
		t.Fatal("root has no subcommands")
	}
}

// TestBareInvocationShowsHelp checks parity with the old dispatcher: bare
// `weave` prints the banner + usage and exits 0.
func TestBareInvocationShowsHelp(t *testing.T) {
	root := NewRootCommand()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(nil)
	if err := root.Execute(); err != nil {
		t.Fatalf("bare invocation errored: %v", err)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Errorf("bare invocation output missing usage:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Weave") {
		t.Errorf("bare invocation output missing banner:\n%s", out.String())
	}
}

// TestUnknownSubcommandErrors checks unknown verbs still fail.
func TestUnknownSubcommandErrors(t *testing.T) {
	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"bogus"})
	if err := root.Execute(); err == nil {
		t.Fatal("unknown subcommand did not error")
	}
}

// TestFlagDefaults spot-checks that defaults transcribed from the old
// dispatcher survived the port.
func TestFlagDefaults(t *testing.T) {
	root := NewRootCommand()
	cases := []struct {
		command string
		flag    string
		def     string
	}{
		{"create", "disk-size", "50"},
		{"create", "disk-format", "raw"},
		{"clone", "concurrency", "4"},
		{"clone", "prune-limit", "100"},
		{"stop", "timeout", "30"},
		{"ip", "resolver", "dhcp"},
		{"pull", "concurrency", "4"},
		{"prune", "entries", "caches"},
		{"serve", "port", "7777"},
		{"setup", "mode", "preset"},
		{"setup", "max-iterations", "200"},
		{"get", "format", "text"},
		{"list", "format", "text"},
	}
	for _, tc := range cases {
		cmd, _, err := root.Find([]string{tc.command})
		if err != nil {
			t.Errorf("command %q not found: %v", tc.command, err)
			continue
		}
		flag := cmd.Flags().Lookup(tc.flag)
		if flag == nil {
			t.Errorf("%s: flag --%s not registered", tc.command, tc.flag)
			continue
		}
		if flag.DefValue != tc.def {
			t.Errorf("%s --%s: default %q, want %q", tc.command, tc.flag, flag.DefValue, tc.def)
		}
	}
}

// TestShorthands checks the preserved short flags.
func TestShorthands(t *testing.T) {
	root := NewRootCommand()
	cases := []struct {
		command   string
		shorthand string
		flag      string
	}{
		{"list", "q", "quiet"},
		{"images", "q", "quiet"},
		{"logs", "f", "follow"},
		{"stop", "t", "timeout"},
	}
	for _, tc := range cases {
		cmd, _, err := root.Find([]string{tc.command})
		if err != nil {
			t.Errorf("command %q not found: %v", tc.command, err)
			continue
		}
		flag := cmd.Flags().ShorthandLookup(tc.shorthand)
		if flag == nil || flag.Name != tc.flag {
			t.Errorf("%s -%s: want --%s, got %v", tc.command, tc.shorthand, tc.flag, flag)
		}
	}
}

// TestArgsValidators checks arity errors fire before any RunE (whose
// lifecycle wrappers would exit the process).
func TestArgsValidators(t *testing.T) {
	cases := [][]string{
		{"stop"},                    // needs 1
		{"rename", "only-one"},      // needs 2
		{"import", "a", "b", "c"},   // needs 2
		{"list", "unexpected"},      // needs 0
		{"export"},                  // needs 1-2
		{"delete"},                  // needs 1+
		{"get", "a", "b"},           // needs 1
		{"images"},                  // needs 1
		{"logs"},                    // needs 1
		{"hvmm", "a", "b", "extra"}, // max 2
	}
	for _, args := range cases {
		root := NewRootCommand()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(args)
		if err := root.Execute(); err == nil {
			t.Errorf("args %v: expected arity error, got nil", args)
		}
	}
}

// TestVersionFlag checks `--version` prints the bare version string.
func TestVersionFlag(t *testing.T) {
	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--version errored: %v", err)
	}
	if strings.Count(strings.TrimSpace(out.String()), "\n") != 0 {
		t.Errorf("--version output not a single line: %q", out.String())
	}
}

// TestHelpForSubcommand checks per-verb help renders with the flag list.
func TestHelpForSubcommand(t *testing.T) {
	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"help", "create"})
	if err := root.Execute(); err != nil {
		t.Fatalf("help create errored: %v", err)
	}
	for _, want := range []string{"weave create", "--from-ipsw", "--disk-size"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help create output missing %q:\n%s", want, out.String())
		}
	}
}
