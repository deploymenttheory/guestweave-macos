//go:build darwin

package winimage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// MediaConfig is the on-disk JSON or YAML configuration for Windows install
// media acquisition. It describes the two supported pathways:
//
//   - Plain:    omit UnattendFile.
//   - Unattend: set UnattendFile to the path of an autounattend.xml.
//
// Example JSON:
//
//	{
//	  "edition": "Professional",
//	  "language": "en-us",
//	  "unattend_file": "/path/to/autounattend.xml"
//	}
//
// Example YAML:
//
//	edition: Professional
//	language: en-us
//	unattend_file: /path/to/autounattend.xml
type MediaConfig struct {
	// Edition is the Windows edition label for display; defaults to
	// "Professional". The downloaded ISO is multi-edition so this does not
	// restrict what editions are available after install.
	Edition string `json:"edition,omitempty" yaml:"edition,omitempty"`
	// Language selects the ISO language. Accepts BCP-47 ("en-us") or the
	// localised name ("English (United States)"). Defaults to "en-us".
	Language string `json:"language,omitempty" yaml:"language,omitempty"`
	// UnattendFile is an optional path to an autounattend.xml. Relative paths
	// are resolved relative to the config file's directory.
	UnattendFile string `json:"unattend_file,omitempty" yaml:"unattend_file,omitempty"`
}

// LoadMediaConfig reads a JSON or YAML MediaConfig from path. The format is
// selected by the file extension (.json → JSON; .yaml/.yml → YAML).
func LoadMediaConfig(path string) (*MediaConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("winimage: read config %s: %w", path, err)
	}
	var cfg MediaConfig
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("winimage: parse YAML config %s: %w", path, err)
		}
	case ".json":
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("winimage: parse JSON config %s: %w", path, err)
		}
	default:
		return nil, fmt.Errorf("winimage: config %s: unsupported extension (use .json, .yaml, or .yml)", path)
	}

	// Resolve relative UnattendFile paths relative to the config directory.
	if cfg.UnattendFile != "" && !filepath.IsAbs(cfg.UnattendFile) {
		cfg.UnattendFile = filepath.Join(filepath.Dir(path), cfg.UnattendFile)
	}
	return &cfg, nil
}
