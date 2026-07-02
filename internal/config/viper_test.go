//go:build darwin

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnvOverrides pins the GUESTWEAVE_AREA_SETTING ↔ area.setting mapping.
// AutomaticEnv reads the environment at Get time, so t.Setenv works
// regardless of when Load first ran in this process.
func TestEnvOverrides(t *testing.T) {
	t.Setenv("GUESTWEAVE_STORAGE_HOME", "/tmp/env-home")
	t.Setenv("GUESTWEAVE_PRUNE_AUTO", "false")
	t.Setenv("GUESTWEAVE_CLIPBOARD_DEBUG", "true")
	t.Setenv("GUESTWEAVE_QEMU_IMG", "/opt/qemu-img")
	t.Setenv("GUESTWEAVE_HVMM_WATCHDOG_MS", "750")

	if got := StorageHome(); got != "/tmp/env-home" {
		t.Errorf("StorageHome() = %q, want /tmp/env-home", got)
	}
	if PruneAuto() {
		t.Error("PruneAuto() = true, want false from GUESTWEAVE_PRUNE_AUTO=false")
	}
	if !ClipboardDebug() {
		t.Error("ClipboardDebug() = false, want true")
	}
	if got := QEMUImg(); got != "/opt/qemu-img" {
		t.Errorf("QEMUImg() = %q, want /opt/qemu-img", got)
	}
	if got := HVMMWatchdogMS(); got != 750 {
		t.Errorf("HVMMWatchdogMS() = %d, want 750", got)
	}
}

// TestPruneAutoDefault pins the positive-boolean default (the old
// WEAVE_NO_AUTO_PRUNE semantics inverted).
func TestPruneAutoDefault(t *testing.T) {
	t.Setenv("GUESTWEAVE_PRUNE_AUTO", "")
	os.Unsetenv("GUESTWEAVE_PRUNE_AUTO")
	if !PruneAuto() {
		t.Error("PruneAuto() = false with no env/file, want default true")
	}
}

// TestSaveMergePreservesForeignKeys pins that Settings.Save keeps the
// viper-managed areas (qemu:, hvmm:, prune:, ...) it does not own.
func TestSaveMergePreservesForeignKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "weave", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "qemu:\n  img: /opt/custom/qemu-img\nprune:\n  auto: false\ncacheDir: /tmp/old-cache\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	s := &Settings{CacheDir: "/tmp/new-cache", DefaultStorage: "main"}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	for _, want := range []string{"img: /opt/custom/qemu-img", "auto: false", "cacheDir: /tmp/new-cache", "defaultStorage: main"} {
		if !strings.Contains(out, want) {
			t.Errorf("saved file missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/tmp/old-cache") {
		t.Errorf("saved file kept the overwritten settings-owned value:\n%s", out)
	}
}
