// Application configuration handlers: effective config, storage locations,
// cache directory, the ghcr registry default, and bridgeable interfaces.
//go:build darwin

package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"
	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	weavenetwork "github.com/deploymenttheory/guestweave/internal/network"
	"github.com/deploymenttheory/guestweave/internal/objcutil"
)

func (s *APIServer) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *APIServer) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[configUpdateRequest](w, r)
	if !ok {
		return
	}
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	if request.DefaultStorage != nil {
		settings.DefaultStorage = *request.DefaultStorage
	}
	if request.CacheDir != nil {
		settings.CacheDir = *request.CacheDir
	}
	if request.Registry != nil {
		settings.Registry = &weaveconfig.RegistrySettings{
			Host:         request.Registry.Host,
			Organization: request.Registry.Organization,
		}
	}
	if err := settings.Save(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *APIServer) handleListLocations(w http.ResponseWriter, r *http.Request) {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	locations := settings.StorageLocations
	if locations == nil {
		locations = map[string]string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"locations": locations,
		"default":   settings.DefaultStorage,
	})
}

func (s *APIServer) handleAddLocation(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[storageLocationRequest](w, r)
	if !ok {
		return
	}
	if request.Name == "" || request.Path == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name and path are required"})
		return
	}
	if !weaveconfig.StorageLocationNamePattern.MatchString(request.Name) {
		writeError(w, weaveerrors.ErrInvalidStorageLocation(request.Name))
		return
	}
	path := objcutil.ExpandTilde(request.Path)
	if err := weaveconfig.ValidateStorageLocation(path); err != nil {
		writeError(w, err)
		return
	}
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	if settings.StorageLocations == nil {
		settings.StorageLocations = map[string]string{}
	}
	settings.StorageLocations[request.Name] = path
	if err := settings.Save(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{request.Name: path})
}

func (s *APIServer) handleRemoveLocation(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	if _, ok := settings.StorageLocations[name]; !ok {
		writeError(w, weaveerrors.ErrStorageLocationNotFound(name))
		return
	}
	delete(settings.StorageLocations, name)
	if settings.DefaultStorage == name {
		settings.DefaultStorage = ""
	}
	if err := settings.Save(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"removed": name})
}

func (s *APIServer) handleDefaultLocation(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	path, err := settings.ResolveStorageLocation(name)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := weaveconfig.ValidateStorageLocation(path); err != nil {
		writeError(w, err)
		return
	}
	settings.DefaultStorage = name
	if err := settings.Save(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"default": name})
}

func (s *APIServer) handleGetCache(w http.ResponseWriter, r *http.Request) {
	config, err := weaveconfig.NewConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"cacheDir": config.WeaveCacheDir})
}

func (s *APIServer) handleSetCache(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[cacheDirRequest](w, r)
	if !ok {
		return
	}
	if request.CacheDir == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "cacheDir is required"})
		return
	}
	path := objcutil.ExpandTilde(request.CacheDir)
	if err := weaveconfig.ValidateStorageLocation(path); err != nil {
		writeError(w, err)
		return
	}
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	settings.CacheDir = path
	if err := settings.Save(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"cacheDir": path})
}

func (s *APIServer) handleGHCR(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[ghcrRequest](w, r)
	if !ok {
		return
	}
	host := request.Registry
	if host == "" {
		host = "ghcr.io"
	}
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	settings.Registry = &weaveconfig.RegistrySettings{
		Host:         host,
		Organization: request.Organization,
	}
	if err := settings.Save(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings.Registry)
}

func (s *APIServer) handleNetworkInterfaces(w http.ResponseWriter, r *http.Request) {
	interfaces := weavenetwork.BridgeInterfaces()
	if interfaces == nil {
		interfaces = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"interfaces": interfaces})
}

// loggingResponse reports the effective log size cap (MB; 0 = unlimited) and
// rotation policy.
func loggingResponse(settings *weaveconfig.Settings) map[string]any {
	return map[string]any{
		"maxSizeMB":   settings.LogMaxSizeBytes() / (1024 * 1024),
		"keepRotated": settings.LogKeepRotated(),
	}
}

func (s *APIServer) handleGetLogging(w http.ResponseWriter, r *http.Request) {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, loggingResponse(settings))
}

func (s *APIServer) handleSetLogging(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[loggingRequest](w, r)
	if !ok {
		return
	}
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	if request.MaxSizeMB != nil {
		if *request.MaxSizeMB < 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "maxSizeMB must be a non-negative integer (0 = unlimited)"})
			return
		}
		if settings.Logging == nil {
			settings.Logging = &weaveconfig.LoggingSettings{}
		}
		settings.Logging.MaxSizeMB = request.MaxSizeMB
	}
	if request.KeepRotated != nil {
		if settings.Logging == nil {
			settings.Logging = &weaveconfig.LoggingSettings{}
		}
		settings.Logging.KeepRotated = request.KeepRotated
	}
	if err := settings.Save(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, loggingResponse(settings))
}

// handleRegistryStatus reports the default (legacy) registry settings, with
// ghcr.io as the implicit default.
func (s *APIServer) handleRegistryStatus(w http.ResponseWriter, r *http.Request) {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	host, organization := "ghcr.io", ""
	if settings.Registry != nil {
		if settings.Registry.Host != "" {
			host = settings.Registry.Host
		}
		organization = settings.Registry.Organization
	}
	writeJSON(w, http.StatusOK, map[string]string{"host": host, "organization": organization})
}

func (s *APIServer) handleListRegistryProfiles(w http.ResponseWriter, r *http.Request) {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	profiles := settings.RegistryProfiles()
	if profiles == nil {
		profiles = []weaveconfig.RegistryProfile{}
	}
	defaultName := ""
	if profile, ok := settings.DefaultRegistryProfile(); ok {
		defaultName = profile.Name
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": profiles, "default": defaultName})
}

func (s *APIServer) handleAddRegistryProfile(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[registryProfileRequest](w, r)
	if !ok {
		return
	}
	if request.Name == "" || request.Organization == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name and organization are required"})
		return
	}
	host := request.Host
	if host == "" {
		host = "ghcr.io"
	}
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	profile := weaveconfig.RegistryProfile{
		Name: request.Name, Host: host, Organization: request.Organization,
		IsInsecure: request.Insecure, IsDefault: request.Default,
	}
	profiles := settings.RegistryProfiles()
	replaced := false
	for index := range profiles {
		if request.Default {
			profiles[index].IsDefault = false
		}
		if profiles[index].Name == request.Name {
			profiles[index] = profile
			replaced = true
		}
	}
	if !replaced {
		profiles = append(profiles, profile)
	}
	settings.Registries = profiles
	if err := settings.ValidateRegistryProfiles(); err != nil {
		writeError(w, err)
		return
	}
	if err := settings.Save(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, profile)
}

func (s *APIServer) handleRemoveRegistryProfile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
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
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "no registry profile named " + name})
		return
	}
	settings.Registries = kept
	if err := settings.Save(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"removed": name})
}

func (s *APIServer) handleDefaultRegistryProfile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	profiles := settings.RegistryProfiles()
	found := false
	for index := range profiles {
		profiles[index].IsDefault = profiles[index].Name == name
		found = found || profiles[index].IsDefault
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "no registry profile named " + name})
		return
	}
	settings.Registries = profiles
	if err := settings.Save(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"default": name})
}
