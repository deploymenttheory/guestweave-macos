// Port of lume's Commands/Config.swift: get or set application
// configuration. Each verb is a typed function invoked by the CLI subtree
// (internal/cli/config.go); they load settings themselves because the config
// command must not silently degrade on a broken settings file — it is the
// tool used to fix it.
//
// lume's "config telemetry" (OTel covers observability here) and
// "config registry gcs" (no GCS backend) are deliberately not ported.
//go:build darwin

package command

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/deploymenttheory/guestweave/internal/clipboardpolicy"
	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"
	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	weavenetwork "github.com/deploymenttheory/guestweave/internal/network"
	"github.com/deploymenttheory/guestweave/internal/objcutil"
)

// ConfigGet shows the effective configuration.
func ConfigGet() error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	config, err := weaveconfig.NewConfig()
	if err != nil {
		return err
	}

	defaultStorage := settings.DefaultStorage
	if defaultStorage == "" {
		defaultStorage = "<unset>"
	}
	fmt.Printf("Home directory: %s\n", config.WeaveHomeDir)
	fmt.Printf("Default storage: %s\n", defaultStorage)
	fmt.Printf("Cache directory: %s\n", config.WeaveCacheDir)

	host, organization := "ghcr.io", "<unset>"
	if settings.Registry != nil {
		if settings.Registry.Host != "" {
			host = settings.Registry.Host
		}
		if settings.Registry.Organization != "" {
			organization = settings.Registry.Organization
		}
	}
	fmt.Printf("Registry: %s\n", host)
	fmt.Printf("Organization: %s\n", organization)

	if len(settings.StorageLocations) > 0 {
		fmt.Println("Storage locations:")
		for _, name := range sortedKeys(settings.StorageLocations) {
			marker := ""
			if name == settings.DefaultStorage {
				marker = " (default)"
			}
			fmt.Printf("  %s: %s%s\n", name, settings.StorageLocations[name], marker)
		}
	}
	return nil
}

// ConfigStorageList lists the named storage locations.
func ConfigStorageList() error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	if len(settings.StorageLocations) == 0 {
		fmt.Println("No storage locations defined.")
		return nil
	}
	for _, name := range sortedKeys(settings.StorageLocations) {
		marker := ""
		if name == settings.DefaultStorage {
			marker = " (default)"
		}
		fmt.Printf("%s: %s%s\n", name, settings.StorageLocations[name], marker)
	}
	return nil
}

// ConfigStorageAdd adds a named storage location.
func ConfigStorageAdd(name, path string) error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	path = objcutil.ExpandTilde(path)
	if !weaveconfig.StorageLocationNamePattern.MatchString(name) {
		return weaveerrors.ErrInvalidStorageLocation(name)
	}
	if err := weaveconfig.ValidateStorageLocation(path); err != nil {
		return err
	}
	if settings.StorageLocations == nil {
		settings.StorageLocations = map[string]string{}
	}
	settings.StorageLocations[name] = path
	if err := settings.Save(); err != nil {
		return err
	}
	fmt.Printf("Added storage location %q: %s\n", name, path)
	return nil
}

// ConfigStorageRemove removes a named storage location.
func ConfigStorageRemove(name string) error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	if _, ok := settings.StorageLocations[name]; !ok {
		return weaveerrors.ErrStorageLocationNotFound(name)
	}
	delete(settings.StorageLocations, name)
	if settings.DefaultStorage == name {
		settings.DefaultStorage = ""
	}
	if err := settings.Save(); err != nil {
		return err
	}
	fmt.Printf("Removed storage location %q\n", name)
	return nil
}

// ConfigStorageDefault sets the default storage location.
func ConfigStorageDefault(name string) error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	path, err := settings.ResolveStorageLocation(name)
	if err != nil {
		return err
	}
	if err := weaveconfig.ValidateStorageLocation(path); err != nil {
		return err
	}
	settings.DefaultStorage = name
	if err := settings.Save(); err != nil {
		return err
	}
	fmt.Printf("Default storage set to %q (%s)\n", name, path)
	fmt.Println("Note: the GUESTWEAVE_STORAGE_HOME environment variable, when set, takes precedence.")
	return nil
}

// ConfigCacheDirShow prints the effective cache directory.
func ConfigCacheDirShow() error {
	config, err := weaveconfig.NewConfig()
	if err != nil {
		return err
	}
	fmt.Println(config.WeaveCacheDir)
	return nil
}

// ConfigCacheDirSet persists a new cache directory.
func ConfigCacheDirSet(path string) error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	path = objcutil.ExpandTilde(path)
	if err := weaveconfig.ValidateStorageLocation(path); err != nil {
		return err
	}
	settings.CacheDir = path
	if err := settings.Save(); err != nil {
		return err
	}
	fmt.Printf("Cache directory set to %s\n", path)
	return nil
}

// ConfigLoggingShow prints the effective logging settings.
func ConfigLoggingShow() error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	fmt.Printf("Max size: %d MB (0 = unlimited)\n", settings.LogMaxSizeBytes()/(1024*1024))
	fmt.Printf("Keep rotated: %t\n", settings.LogKeepRotated())
	return nil
}

// ConfigLoggingMaxSizeShow prints the per-file log cap in MB.
func ConfigLoggingMaxSizeShow() error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	fmt.Println(settings.LogMaxSizeBytes() / (1024 * 1024))
	return nil
}

// ConfigLoggingMaxSizeSet persists the per-file log cap in MB (0 = unlimited).
func ConfigLoggingMaxSizeSet(mb int) error {
	if mb < 0 {
		return weaveerrors.ErrGeneric("maxSizeMB must be a non-negative integer (0 = unlimited)")
	}
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	if settings.Logging == nil {
		settings.Logging = &weaveconfig.LoggingSettings{}
	}
	settings.Logging.MaxSizeMB = &mb
	if err := settings.Save(); err != nil {
		return err
	}
	fmt.Printf("Log max size set to %d MB\n", mb)
	return nil
}

// ConfigLoggingKeepRotatedShow prints the rotation retention flag.
func ConfigLoggingKeepRotatedShow() error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	fmt.Printf("%t\n", settings.LogKeepRotated())
	return nil
}

// ConfigLoggingKeepRotatedSet persists the rotation retention flag.
func ConfigLoggingKeepRotatedSet(keep bool) error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	if settings.Logging == nil {
		settings.Logging = &weaveconfig.LoggingSettings{}
	}
	settings.Logging.KeepRotated = &keep
	if err := settings.Save(); err != nil {
		return err
	}
	fmt.Printf("Log keepRotated set to %t\n", keep)
	return nil
}

// ConfigRegistryList lists the configured registry profiles.
func ConfigRegistryList() error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	profiles := settings.RegistryProfiles()
	if len(profiles) == 0 {
		fmt.Println("No registry profiles configured.")
		return nil
	}
	for _, profile := range profiles {
		marker := " "
		if profile.IsDefault {
			marker = "*"
		}
		insecure := ""
		if profile.IsInsecure {
			insecure = " (insecure)"
		}
		fmt.Printf(
			"%s %-16s %s/%s%s\n",
			marker,
			profile.Name,
			profile.Host,
			profile.Organization,
			insecure,
		)
	}
	return nil
}

// ConfigRegistryAdd adds or replaces a registry profile.
func ConfigRegistryAdd(profile weaveconfig.RegistryProfile) error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	profiles := settings.RegistryProfiles()
	replaced := false
	for index := range profiles {
		if profile.IsDefault {
			profiles[index].IsDefault = false
		}
		if profiles[index].Name == profile.Name {
			profiles[index] = profile
			replaced = true
		}
	}
	if !replaced {
		profiles = append(profiles, profile)
	}
	settings.Registries = profiles
	if err := settings.ValidateRegistryProfiles(); err != nil {
		return err
	}
	if err := settings.Save(); err != nil {
		return err
	}
	fmt.Printf("Registry profile %q -> %s/%s\n", profile.Name, profile.Host, profile.Organization)
	return nil
}

// ConfigRegistryRemove removes a registry profile.
func ConfigRegistryRemove(name string) error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	profiles := settings.RegistryProfiles()
	kept := profiles[:0]
	removed := false
	for _, profile := range profiles {
		if profile.Name == name {
			removed = true
			continue
		}
		kept = append(kept, profile)
	}
	if !removed {
		return weaveerrors.ErrGeneric("no registry profile named %q", name)
	}
	settings.Registries = kept
	if err := settings.Save(); err != nil {
		return err
	}
	fmt.Printf("Removed registry profile %q\n", name)
	return nil
}

// ConfigRegistryDefault marks a registry profile as the default.
func ConfigRegistryDefault(name string) error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	profiles := settings.RegistryProfiles()
	found := false
	for index := range profiles {
		profiles[index].IsDefault = profiles[index].Name == name
		found = found || profiles[index].IsDefault
	}
	if !found {
		return weaveerrors.ErrGeneric("no registry profile named %q", name)
	}
	settings.Registries = profiles
	if err := settings.Save(); err != nil {
		return err
	}
	fmt.Printf("Default registry profile is now %q\n", name)
	return nil
}

// ConfigRegistryStatus prints the legacy registry defaults.
func ConfigRegistryStatus() error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	if settings.Registry == nil {
		fmt.Println("Registry: ghcr.io (default)")
		fmt.Println("Organization: <unset>")
		return nil
	}
	host := settings.Registry.Host
	if host == "" {
		host = "ghcr.io"
	}
	organization := settings.Registry.Organization
	if organization == "" {
		organization = "<unset>"
	}
	fmt.Printf("Registry: %s\n", host)
	fmt.Printf("Organization: %s\n", organization)
	return nil
}

// ConfigRegistryGHCR persists the legacy registry defaults.
func ConfigRegistryGHCR(host, organization string) error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	settings.Registry = &weaveconfig.RegistrySettings{Host: host, Organization: organization}
	if err := settings.Save(); err != nil {
		return err
	}
	fmt.Printf("Registry set to %s (organization: %s)\n", host, organization)
	return nil
}

// ConfigNetworkInterfaces lists the bridgeable host interfaces.
func ConfigNetworkInterfaces() error {
	interfaces := weavenetwork.BridgeInterfaces()
	if len(interfaces) == 0 {
		fmt.Fprintln(os.Stderr, "No bridgeable network interfaces found.")
		return nil
	}
	for _, description := range interfaces {
		fmt.Println(description)
	}
	return nil
}

// ConfigClipboardShow prints the effective default clipboard policy.
func ConfigClipboardShow() error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	base := clipboardpolicy.Default()
	if settings.DefaultClipboardPolicy != nil {
		base = *settings.DefaultClipboardPolicy
	}
	printClipboardPolicy(base)
	return nil
}

// ConfigClipboardReset clears the default clipboard policy back to built-in.
func ConfigClipboardReset() error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	settings.DefaultClipboardPolicy = nil
	if err := settings.Save(); err != nil {
		return err
	}
	fmt.Println("Default clipboard policy reset to the built-in default.")
	return nil
}

// ConfigClipboardSet applies flag overrides onto the default clipboard policy
// and persists it. A zero override prints the effective policy instead.
func ConfigClipboardSet(v ClipboardFlagValues) error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}

	override := v.Override()
	base := clipboardpolicy.Default()
	if settings.DefaultClipboardPolicy != nil {
		base = *settings.DefaultClipboardPolicy
	}
	if override.IsZero() {
		// No flags: show the effective default policy.
		printClipboardPolicy(base)
		return nil
	}

	updated := override.Apply(base)
	settings.DefaultClipboardPolicy = &updated
	if err := settings.Save(); err != nil {
		return err
	}
	fmt.Println("Default clipboard policy updated:")
	printClipboardPolicy(updated)
	return nil
}

func printClipboardPolicy(p clipboardpolicy.Policy) {
	fmt.Printf("Enabled: %t\n", p.Enabled)
	fmt.Printf("Direction: %s\n", p.Direction)
	fmt.Printf("Formats: plainText=%t richText=%t image=%t\n",
		p.Formats.PlainText, p.Formats.RichText, p.Formats.Image)
	fmt.Printf("File transfer: %t\n", p.FileTransfer)
	if len(p.AllowedTypes) > 0 {
		fmt.Printf("Allowed types: %s\n", strings.Join(p.AllowedTypes, ", "))
	}
	fmt.Printf("Max content bytes: %d\n", p.MaxBytes())
	if bps := p.BytesPerSec(); bps > 0 {
		fmt.Printf("Bandwidth: %d Mbps × %d%% = %d bytes/sec\n", p.SessionMbps, p.BandwidthPct, bps)
	} else {
		fmt.Println("Bandwidth: unlimited")
	}
	fmt.Printf("Audit log: %t\n", p.AuditLog)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
