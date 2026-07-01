// VM archive import/export. Both support server-side filesystem paths and an
// HTTP body channel (streamed multipart upload / streamed download), so the
// API works for both local and remote clients.
//go:build darwin

package httpapi

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/go-chi/chi/v5"

	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/deploymenttheory/guestweave/internal/vmstorage"
)

// handleImport imports from a server-side .tvm path.
func (s *APIServer) handleImport(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[importRequest](w, r)
	if !ok {
		return
	}
	if request.Path == "" || request.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "path and name are required"})
		return
	}
	if err := s.runImport(r, request.Path, request.Name); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": request.Name})
}

// handleImportUpload streams a multipart .tvm upload to a temp file, then
// imports it. The VM name comes from the ?name= query parameter.
func (s *APIServer) handleImportUpload(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		writeJSON(
			w,
			http.StatusBadRequest,
			errorResponse{Error: "name query parameter is required"},
		)
		return
	}
	reader, err := r.MultipartReader()
	if err != nil {
		writeJSON(
			w,
			http.StatusBadRequest,
			errorResponse{Error: "expected a multipart upload: " + err.Error()},
		)
		return
	}

	tmp, err := os.CreateTemp("", "weave-import-*.tvm")
	if err != nil {
		writeError(w, err)
		return
	}
	defer os.Remove(tmp.Name())

	wrote := false
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			tmp.Close()
			writeError(w, err)
			return
		}
		if part.FormName() != "file" {
			continue
		}
		if _, err := io.Copy(tmp, part); err != nil {
			tmp.Close()
			writeError(w, err)
			return
		}
		wrote = true
		break
	}
	if err := tmp.Close(); err != nil {
		writeError(w, err)
		return
	}
	if !wrote {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing \"file\" part in upload"})
		return
	}

	if err := s.runImport(r, tmp.Name(), name); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": name})
}

func (s *APIServer) runImport(r *http.Request, path, name string) error {
	command := &weavecommand.ImportCommand{Path: path, Name: name}
	if err := command.Validate(); err != nil {
		return err
	}
	return command.Run(r.Context())
}

// handleExport exports to a server-side path (Force-skipping the overwrite
// prompt, which a server cannot answer).
func (s *APIServer) handleExport(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[struct {
		Path string `json:"path"`
	}](w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	command := &weavecommand.ExportCommand{Name: name, Path: request.Path, Force: true}
	if err := command.Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	path := request.Path
	if path == "" {
		path = name + ".tvm"
	}
	writeJSON(w, http.StatusOK, map[string]string{"exported": name, "path": path})
}

// handleExportDownload exports to a temp archive and streams it as the
// response body.
func (s *APIServer) handleExportDownload(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	dir, err := os.MkdirTemp("", "weave-export-*")
	if err != nil {
		writeError(w, err)
		return
	}
	defer os.RemoveAll(dir)
	archivePath := filepath.Join(dir, name+".tvm")

	vmDir, err := vmstorage.VMStorageHelperOpen(name)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := vmDir.ExportToArchive(archivePath); err != nil {
		writeError(w, err)
		return
	}

	file, err := os.Open(archivePath)
	if err != nil {
		writeError(w, err)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+".tvm\"")
	if info, err := file.Stat(); err == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	}
	_, _ = io.Copy(w, file)
}
