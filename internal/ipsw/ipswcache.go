// Port of tart's IPSWCache.swift: the ~/.weave/cache/IPSWs storage.
//go:build darwin

package ipsw

import (
	"os"
	"path/filepath"
	"strings"

	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	"github.com/deploymenttheory/weave/internal/prune"
)

// IPSWCache ports tart's IPSWCache class.
type IPSWCache struct {
	BaseURL string
}

var _ prune.PrunableStorage = (*IPSWCache)(nil)

// NewIPSWCache ports IPSWCache.init().
func NewIPSWCache() (*IPSWCache, error) {
	config, err := weaveconfig.NewConfig()
	if err != nil {
		return nil, err
	}
	baseURL := filepath.Join(config.WeaveCacheDir, "IPSWs")
	if err := os.MkdirAll(baseURL, 0o755); err != nil {
		return nil, err
	}
	return &IPSWCache{BaseURL: baseURL}, nil
}

// LocationFor ports IPSWCache.locationFor(fileName:).
func (c *IPSWCache) LocationFor(fileName string) string {
	return filepath.Join(c.BaseURL, fileName)
}

// Prunables ports IPSWCache.prunables(): every *.ipsw file in the cache.
func (c *IPSWCache) Prunables() ([]prune.Prunable, error) {
	entries, err := os.ReadDir(c.BaseURL)
	if err != nil {
		return nil, err
	}

	var prunables []prune.Prunable
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".ipsw") {
			prunables = append(prunables, prune.NewPrunableURL(filepath.Join(c.BaseURL, entry.Name())))
		}
	}
	return prunables, nil
}
