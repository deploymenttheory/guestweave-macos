// Viper-backed application configuration: one strict naming convention for
// every first-party knob. A viper key "area.setting" is settable from the
// environment as GUESTWEAVE_AREA_SETTING or from the settings file
// (~/.config/weave/config.yaml) under "area: setting:", with precedence
// env > file > default.
//
// Every consumer goes through the typed accessors below — no raw viper.Get
// outside this file — so the key inventory lives in one place. External
// conventions (OTEL_*, SENTRY_*, NO_COLOR, XDG_CONFIG_HOME,
// ANTHROPIC_API_KEY, HTTP(S)_PROXY) are deliberately not routed through here,
// and the GUESTWEAVE_REGISTRY_* credentials stay direct environment reads in
// internal/credentials (env-only by design: never file-settable, never
// persisted).
//go:build darwin

package config

import (
	"strings"
	"sync"

	"github.com/spf13/viper"
)

// EnvPrefix is the environment prefix for all first-party variables:
// GUESTWEAVE_<AREA>_<SETTING> ↔ viper key "<area>.<setting>".
const EnvPrefix = "GUESTWEAVE"

var (
	v        = viper.New()
	loadOnce sync.Once
)

// Load initializes defaults, environment binding and the config file. It is
// called lazily by every accessor (and eagerly by the CLI entry point), so
// ordering is never a concern; repeated calls are no-ops.
func Load() {
	loadOnce.Do(func() {
		v.SetEnvPrefix(EnvPrefix)
		v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
		v.AutomaticEnv()

		v.SetDefault("prune.auto", true)

		// The settings file doubles as the config file for these keys; the
		// Settings struct (settings.go) keeps its own yaml.v3 loader for the
		// structured blocks it owns. A missing or broken file is tolerated
		// here — settingsOrWarn owns the warning.
		if path, err := settingsPath(); err == nil {
			v.SetConfigFile(path)
			v.SetConfigType("yaml")
			_ = v.ReadInConfig()
		}
	})
}

// reloadConfigFile re-reads the config file after a Settings.Save() so the
// viper view stays consistent within long-lived processes (weave serve).
func reloadConfigFile() {
	Load()
	_ = v.ReadInConfig()
}

// StorageHome overrides the weave home directory (was WEAVE_HOME).
func StorageHome() string { Load(); return v.GetString("storage.home") }

// PruneAuto reports whether automatic cache pruning is enabled (default
// true; replaces the negated WEAVE_NO_AUTO_PRUNE).
func PruneAuto() bool { Load(); return v.GetBool("prune.auto") }

// ClipboardDebug enables verbose clipboard sync tracing (was WEAVE_CLIP_DEBUG).
func ClipboardDebug() bool { Load(); return v.GetBool("clipboard.debug") }

// ClipboardAudit force-enables the clipboard transfer audit log (was
// WEAVE_CLIP_AUDIT).
func ClipboardAudit() bool { Load(); return v.GetBool("clipboard.audit") }

// QEMU toolchain overrides (were WEAVE_QEMU_*).
func QEMUSystemAarch64() string { Load(); return v.GetString("qemu.system_aarch64") }
func QEMUImg() string           { Load(); return v.GetString("qemu.img") }
func QEMUFirmwareCode() string  { Load(); return v.GetString("qemu.firmware_code") }
func QEMUFirmwareVars() string  { Load(); return v.GetString("qemu.firmware_vars") }

// Experimental Hypervisor.framework backend knobs (were WEAVE_HVMM_*,
// WEAVE_ENTRY_EL1 and WEAVE_WATCHDOG_MS).
func HVMMFirmware() string    { Load(); return v.GetString("hvmm.firmware") }
func HVMMFramebuffer() string { Load(); return v.GetString("hvmm.framebuffer") }
func HVMMDisk() string        { Load(); return v.GetString("hvmm.disk") }
func HVMMNVMe() string        { Load(); return v.GetString("hvmm.nvme") }
func HVMMEntryEL1() bool      { Load(); return v.GetBool("hvmm.entry_el1") }
func HVMMWatchdogMS() int     { Load(); return v.GetInt("hvmm.watchdog_ms") }
