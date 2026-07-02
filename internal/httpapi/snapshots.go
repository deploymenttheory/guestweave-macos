// Disk snapshot handlers: list / create / revert / delete. Thin adapters over
// the exported weavecommand snapshot functions, which handle the running-vs-
// stopped VM logic.
//go:build darwin

package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/deploymenttheory/guestweave/internal/vm/snapshot"
)

func (s *APIServer) handleListSnapshots(w http.ResponseWriter, r *http.Request) {
	snapshots, err := weavecommand.ListSnapshots(chi.URLParam(r, "name"))
	if err != nil {
		writeError(w, err)
		return
	}
	if snapshots == nil {
		snapshots = []snapshot.Snapshot{}
	}
	writeJSON(w, http.StatusOK, snapshots)
}

func (s *APIServer) handleCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[snapshotCreateRequest](w, r)
	if !ok {
		return
	}
	if request.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name is required"})
		return
	}
	snap, err := weavecommand.CreateSnapshot(chi.URLParam(r, "name"), request.Name, request.Description)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, snap)
}

func (s *APIServer) handleRevertSnapshot(w http.ResponseWriter, r *http.Request) {
	if err := weavecommand.RevertSnapshot(chi.URLParam(r, "name"), chi.URLParam(r, "snapshot")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"reverted": chi.URLParam(r, "snapshot")})
}

func (s *APIServer) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	if err := weavecommand.DeleteSnapshot(chi.URLParam(r, "name"), chi.URLParam(r, "snapshot")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": chi.URLParam(r, "snapshot")})
}
