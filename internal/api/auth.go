package api

import (
	"context"
	"net/http"
	"strings"
)

type actorKey struct{}

func actorFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(actorKey{}).(string); ok && v != "" {
		return v
	}
	return "dev"
}

// devAuth is the P0 boundary. OIDC/JWKS/RBAC are P1 work; this middleware must
// not be presented as an identity system.
func (s *Server) devAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || strings.HasSuffix(r.URL.Path, "/log/stream") {
			// EventSource cannot send Authorization. The stream handler validates
			// a short-lived, run-scoped ticket itself.
			next.ServeHTTP(w, r)
			return
		}
		header := r.Header.Get("Authorization")
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		if token == "" || token != s.cfg.DevToken {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		actor := strings.TrimSpace(r.Header.Get("X-Actor"))
		if actor == "" {
			actor = "dev"
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, actor)))
	})
}

func (s *Server) workerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(r.Header.Get("X-Worker-Token"))
		if token == "" {
			token = strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		}
		if token == "" || token != s.cfg.WorkerToken {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid worker token")
			return
		}
		next.ServeHTTP(w, r)
	})
}
