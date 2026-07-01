// Port of tart's Config.swift: resolves the weave home tree (honouring
// WEAVE_HOME), creates the cache and tmp directories, and garbage-collects
// stale tmp entries. Paths are plain strings managed with os/path/filepath.
//go:build darwin

package config

import (
	"os"
	"path/filepath"

	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	weavelock "github.com/deploymenttheory/guestweave/internal/lock"
)

// Config mirrors tart's Config struct.
type Config struct {
	WeaveHomeDir  string
	WeaveCacheDir string
	WeaveTmpDir   string
}

// NewConfig ports Config.init().
func NewConfig() (*Config, error) {
	var weaveHomeDir string

	// Resolution order: WEAVE_HOME env var, then the settings file's default
	// storage location, then ~/.weave.
	if customWeaveHome := os.Getenv("WEAVE_HOME"); customWeaveHome != "" {
		weaveHomeDir = customWeaveHome
		if err := validateWeaveHome(weaveHomeDir); err != nil {
			return nil, err
		}
	} else if settingsHome, ok := settingsOrWarn().DefaultStoragePath(); ok {
		weaveHomeDir = settingsHome
		if err := validateWeaveHome(weaveHomeDir); err != nil {
			return nil, err
		}
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		weaveHomeDir = filepath.Join(home, ".weave")
	}

	weaveCacheDir := filepath.Join(weaveHomeDir, "cache")
	if cacheDir := settingsOrWarn().CacheDir; cacheDir != "" {
		weaveCacheDir = cacheDir
	}
	if err := os.MkdirAll(weaveCacheDir, 0o755); err != nil {
		return nil, err
	}

	weaveTmpDir := filepath.Join(weaveHomeDir, "tmp")
	if err := os.MkdirAll(weaveTmpDir, 0o755); err != nil {
		return nil, err
	}

	return &Config{
		WeaveHomeDir:  weaveHomeDir,
		WeaveCacheDir: weaveCacheDir,
		WeaveTmpDir:   weaveTmpDir,
	}, nil
}

// GC ports Config.gc(): removes every tmp-directory entry whose flock can be
// acquired — i.e. whose creating process has finished or crashed.
func (c *Config) GC() error {
	entries, err := os.ReadDir(c.WeaveTmpDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		path := filepath.Join(c.WeaveTmpDir, entry.Name())

		lock, err := weavelock.NewFileLock(path)
		if err != nil {
			return err
		}

		acquired, err := lock.Trylock()
		if err != nil {
			_ = lock.Close()
			return err
		}
		if !acquired {
			_ = lock.Close()
			continue
		}

		if err := os.RemoveAll(path); err != nil {
			_ = lock.Close()
			return err
		}

		if err := lock.Unlock(); err != nil {
			_ = lock.Close()
			return err
		}
		_ = lock.Close()
	}

	return nil
}

// validateWeaveHome creates weaveHome and any missing parents, naming the path
// when it cannot be created.
func validateWeaveHome(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return weaveerrors.ErrGeneric(
			"WEAVE_HOME is invalid: %s does not exist, yet we can't create it: %v", path, err)
	}
	return nil
}
