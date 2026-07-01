// JSON request/response plumbing shared by all handlers.
//go:build darwin

package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/logging"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var vmErr *weaveerrors.VMError
	if errors.As(err, &vmErr) && vmErr.Kind == weaveerrors.VMErrorNotFound {
		status = http.StatusNotFound
	}
	var usageErr *weaveerrors.UsageError
	if errors.As(err, &usageErr) {
		status = http.StatusBadRequest
	}
	logging.LogError("API error: %v", err)
	writeJSON(w, status, errorResponse{Error: err.Error()})
}

// readJSON decodes the request body into T; a fully empty body yields the
// zero value.
func readJSON[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var value T
	if r.Body == nil {
		return value, true
	}
	err := json.NewDecoder(r.Body).Decode(&value)
	if err != nil && !errors.Is(err, io.EOF) {
		writeJSON(
			w,
			http.StatusBadRequest,
			errorResponse{Error: "invalid JSON body: " + err.Error()},
		)
		return value, false
	}
	return value, true
}
