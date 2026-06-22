// OCI registry handlers: pull (sync + async), push, remote tag listing, the
// latest-IPSW lookup, and registry login/logout.
//go:build darwin

package httpapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	weavecommand "github.com/deploymenttheory/weave/internal/command"
	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	"github.com/deploymenttheory/weave/internal/credentials"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/oci"
	weaveregistry "github.com/deploymenttheory/weave/internal/registry"
)

func (s *APIServer) handleIPSW(w http.ResponseWriter, r *http.Request) {
	image, err := weavecommand.FetchLatestSupportedRestoreImage(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(
		w,
		http.StatusOK,
		map[string]string{"url": objcutil.AbsoluteURLString(image.URL())},
	)
}

func (s *APIServer) pullCommandFrom(request pullVMRequest) *weavecommand.PullCommand {
	concurrency := request.Concurrency
	if concurrency == 0 {
		concurrency = 4
	}
	return &weavecommand.PullCommand{
		RemoteName:  request.Image,
		Insecure:    request.Insecure,
		Concurrency: concurrency,
		Deduplicate: request.Deduplicate,
	}
}

func (s *APIServer) handlePull(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[pullVMRequest](w, r)
	if !ok {
		return
	}
	if request.Image == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "image is required"})
		return
	}
	if err := s.pullCommandFrom(request).Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"pulled": request.Image})
}

func (s *APIServer) handlePullStart(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[pullVMRequest](w, r)
	if !ok {
		return
	}
	if request.Image == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "image is required"})
		return
	}
	command := s.pullCommandFrom(request)
	id := s.pull.start(request.Image, func() error {
		return command.Run(context.Background())
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
}

func (s *APIServer) handlePullStatus(w http.ResponseWriter, r *http.Request) {
	job, ok := s.pull.get(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "no such pull job"})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *APIServer) handlePushVM(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[pushVMRequest](w, r)
	if !ok {
		return
	}
	if request.Name == "" || len(request.Images) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name and images are required"})
		return
	}
	concurrency := request.Concurrency
	if concurrency == 0 {
		concurrency = 4
	}
	command := &weavecommand.PushCommand{
		LocalName:     request.Name,
		RemoteNames:   request.Images,
		Registry:      request.Registry,
		Insecure:      request.Insecure,
		Concurrency:   concurrency,
		ChunkSize:     request.ChunkSize,
		Labels:        request.Labels,
		PopulateCache: request.PopulateCache,
	}
	if err := command.Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"pushed": request.Name})
}

func (s *APIServer) handleImages(w http.ResponseWriter, r *http.Request) {
	repository := r.URL.Query().Get("repository")
	if repository == "" {
		writeJSON(
			w,
			http.StatusBadRequest,
			errorResponse{Error: "repository query parameter is required"},
		)
		return
	}
	insecure := r.URL.Query().Get("insecure") == "true"
	registryProfile := r.URL.Query().Get("registry")

	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	client, remoteName, err := weaveregistry.Resolve(
		repository+":latest",
		registryProfile,
		insecure,
		settings,
	)
	if err != nil {
		writeError(w, weaveerrors.ErrFailedToParseRemoteName(err.Error()))
		return
	}
	tags, err := client.TagsList(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"repository": remoteName.Host + "/" + remoteName.Namespace,
		"tags":       tags,
	})
}

func (s *APIServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[loginRequest](w, r)
	if !ok {
		return
	}
	if request.Host == "" || request.Username == "" || request.Password == "" {
		writeJSON(
			w,
			http.StatusBadRequest,
			errorResponse{Error: "host, username and password are required"},
		)
		return
	}
	if !request.NoValidate {
		provider := &staticCredentialsProvider{
			host:     request.Host,
			user:     request.Username,
			password: request.Password,
		}
		registry, err := oci.NewRegistry(
			request.Host,
			"",
			request.Insecure,
			[]credentials.CredentialsProvider{provider},
		)
		if err != nil {
			writeError(w, err)
			return
		}
		if err := registry.Ping(r.Context()); err != nil {
			writeError(w, weaveerrors.ErrInvalidCredentials("invalid credentials: %v", err))
			return
		}
	}
	if err := (&credentials.KeychainCredentialsProvider{}).Store(
		request.Host,
		request.Username,
		request.Password,
	); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"loggedIn": request.Host})
}

func (s *APIServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	host := chi.URLParam(r, "host")
	if err := (&credentials.KeychainCredentialsProvider{}).Remove(host); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"loggedOut": host})
}

// staticCredentialsProvider supplies a single host's credentials for login
// validation (mirrors the command package's DictionaryCredentialsProvider,
// whose backing map is unexported).
type staticCredentialsProvider struct {
	host     string
	user     string
	password string
}

var _ credentials.CredentialsProvider = (*staticCredentialsProvider)(nil)

func (p *staticCredentialsProvider) UserFriendlyName() string { return "HTTP API login request" }

func (p *staticCredentialsProvider) Retrieve(host string) (string, string, bool, error) {
	if host != p.host {
		return "", "", false, nil
	}
	return p.user, p.password, true, nil
}

func (p *staticCredentialsProvider) Store(string, string, string) error { return nil }
