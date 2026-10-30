package web

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesConsoleShell(t *testing.T) {
	for _, path := range []string{"/", "/index.html", "/job/8fbcdaac", "/workers"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Fatalf("%s content-type=%q", path, ct)
		}
		if rec.Header().Get("Cache-Control") != noCache {
			t.Fatalf("%s cache-control=%q", path, rec.Header().Get("Cache-Control"))
		}
		body, _ := io.ReadAll(rec.Body)
		if !strings.Contains(string(body), `<div id="root">`) {
			t.Fatalf("%s missing React root", path)
		}
	}
}

func TestHandlerServesBuiltAssets(t *testing.T) {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		t.Fatal(err)
	}
	assets, err := fs.Glob(sub, "assets/*")
	if err != nil || len(assets) == 0 {
		t.Fatalf("no built assets found: %v", err)
	}
	for _, asset := range assets {
		req := httptest.NewRequest(http.MethodGet, "/"+asset, nil)
		rec := httptest.NewRecorder()
		Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d", asset, rec.Code)
		}
		if rec.Header().Get("Cache-Control") != immutable {
			t.Fatalf("%s cache-control=%q", asset, rec.Header().Get("Cache-Control"))
		}
	}
}

func TestHandlerMissingAssetIsNotFound(t *testing.T) {
	for _, path := range []string{"/no-such-asset.js", "/assets/missing.js", "/favicon.ico"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d want 404", path, rec.Code)
		}
	}
}
