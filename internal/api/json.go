package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"

	"scheduler-platform/internal/store"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func idFromPath(r *http.Request, name string) (uuid.UUID, error) {
	raw := r.PathValue(name)
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s: %w", name, err)
	}
	return id, nil
}

func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "resource already exists")
	case errors.Is(err, store.ErrLeaseLost):
		writeError(w, http.StatusConflict, "lease_lost", "run lease is no longer valid")
	case errors.Is(err, store.ErrDependencyEdge):
		writeError(w, http.StatusBadRequest, "invalid_dependency", "invalid dependency edge")
	case errors.Is(err, store.ErrDependencyCycle):
		writeError(w, http.StatusConflict, "dependency_cycle", "dependency edges would create a cycle")
	case errors.Is(err, store.ErrDependencyDepth):
		writeError(w, http.StatusBadRequest, "dependency_depth", "dependency depth exceeds 100")
	case errors.Is(err, store.ErrDependencyFanout):
		writeError(w, http.StatusBadRequest, "dependency_fanout", "an upstream job cannot have more than 500 downstream jobs")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "internal server error")
	}
}

func jsonUnmarshal(data []byte, dst any) error {
	return json.Unmarshal(data, dst)
}
