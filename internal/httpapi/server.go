// Package httpapi is weave's REST API server (a port of lume's
// Server/Server.swift + Handlers.swift onto net/http + chi), served under
// /weave/*. It exposes the same VM lifecycle as the CLI; handlers are thin
// adapters over the internal/command command structs and the shared
// internal/vm/service layer. The MCP server lives separately in internal/mcp.
//go:build darwin

package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/logging"
)

// APIServer hosts the REST API.
type APIServer struct {
	port uint16
	pull pullJobs
}

func NewAPIServer(port uint16) *APIServer {
	return &APIServer{port: port}
}

// Run serves until ctx is cancelled.
func (s *APIServer) Run(ctx context.Context) error {
	server := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", s.port),
		Handler: s.router(),
	}

	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return weaveerrors.ErrGeneric(
				"port %d is already in use, try --port %d",
				s.port,
				s.port+1,
			)
		}
		return err
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	fmt.Printf("Serving HTTP API on http://%s\n", server.Addr)
	logging.LogInfo("HTTP API server started on %s", server.Addr)

	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// router builds the chi router and route table for the API. Static path
// segments (e.g. /weave/vms/clone) take precedence over the {name} parameter
// route, so order is not significant.
func (s *APIServer) router() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(requestLogger)

	r.Route("/weave", func(r chi.Router) {
		r.Route("/vms", func(r chi.Router) {
			r.Get("/", s.handleListVMs)
			r.Post("/", s.handleCreateVM)
			r.Post("/clone", s.handleCloneVM)
			r.Post("/push", s.handlePushVM)
			r.Post("/import", s.handleImport)
			r.Post("/import/upload", s.handleImportUpload)
			r.Route("/{name}", func(r chi.Router) {
				r.Get("/", s.handleGetVM)
				r.Delete("/", s.handleDeleteVM)
				r.Patch("/", s.handleUpdateVM)
				r.Post("/run", s.handleRunVM)
				r.Post("/stop", s.handleStopVM)
				r.Post("/suspend", s.handleSuspendVM)
				r.Get("/snapshots", s.handleListSnapshots)
				r.Post("/snapshots", s.handleCreateSnapshot)
				r.Post("/snapshots/{snapshot}/revert", s.handleRevertSnapshot)
				r.Delete("/snapshots/{snapshot}", s.handleDeleteSnapshot)
				r.Post("/rename", s.handleRenameVM)
				r.Post("/setup", s.handleSetupVM)
				r.Post("/clipboard", s.handleSetClipboardPolicy)
				r.Get("/fqn", s.handleFQN)
				r.Get("/ip", s.handleIP)
				r.Post("/exec", s.handleExec)
				r.Get("/exec/ws", s.handleExecWS)
				r.Post("/ssh", s.handleSSH)
				r.Get("/ssh/ws", s.handleSSHWS)
				r.Post("/export", s.handleExport)
				r.Get("/export/download", s.handleExportDownload)
			})
		})

		r.Get("/ipsw", s.handleIPSW)
		r.Post("/pull", s.handlePull)
		r.Post("/pull/start", s.handlePullStart)
		r.Get("/pull/{id}", s.handlePullStatus)
		r.Post("/prune", s.handlePrune)
		r.Get("/images", s.handleImages)
		r.Get("/logs", s.handleLogs)
		r.Get("/host/status", s.handleHostStatus)
		r.Get("/openapi.yaml", s.handleOpenAPI)

		r.Route("/registry", func(r chi.Router) {
			r.Post("/login", s.handleLogin)
			r.Delete("/{host}", s.handleLogout)
		})

		r.Route("/config", func(r chi.Router) {
			r.Get("/", s.handleGetConfig)
			r.Post("/", s.handleUpdateConfig)
			r.Get("/locations", s.handleListLocations)
			r.Post("/locations", s.handleAddLocation)
			r.Delete("/locations/{name}", s.handleRemoveLocation)
			r.Post("/locations/default/{name}", s.handleDefaultLocation)
			r.Get("/cache", s.handleGetCache)
			r.Post("/cache", s.handleSetCache)
			r.Get("/logging", s.handleGetLogging)
			r.Post("/logging", s.handleSetLogging)
			r.Get("/registry", s.handleRegistryStatus)
			r.Post("/registry/ghcr", s.handleGHCR)
			r.Get("/registry/profiles", s.handleListRegistryProfiles)
			r.Post("/registry/profiles", s.handleAddRegistryProfile)
			r.Delete("/registry/profiles/{name}", s.handleRemoveRegistryProfile)
			r.Post("/registry/profiles/default/{name}", s.handleDefaultRegistryProfile)
			r.Get("/network/interfaces", s.handleNetworkInterfaces)
		})
	})

	return r
}

// requestLogger logs each API request through weave's file logger.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logging.LogInfo("API %s %s (%s)", r.Method, r.URL.Path, time.Since(start))
	})
}
