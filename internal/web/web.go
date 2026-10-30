// Package web serves the embedded React control console.
//
// The console is compiled by Vite into static assets and embedded in the API
// binary. It calls the same authenticated /v1 API as any other client. The
// static shell is public; every data and mutation call still requires the
// development bearer token.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed dist
var embedded embed.FS

const (
	noCache = "no-cache"
	// Vite emits content-hashed asset names, so these responses are immutable.
	immutable = "public, max-age=31536000, immutable"
)

// Handler serves the console below the mount point. It must be mounted with
// http.StripPrefix("/ui/", web.Handler()). Unknown extensionless paths fall
// back to index.html so client-side routes survive a refresh or deep link.
func Handler() http.Handler {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic("web: embedded dist directory is missing: " + err.Error())
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic("web: embedded index.html is missing: " + err.Error())
	}
	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" || name == "index.html" {
			serveIndex(w, r, index)
			return
		}
		if info, statErr := fs.Stat(sub, name); statErr == nil && !info.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", immutable)
			} else {
				w.Header().Set("Cache-Control", noCache)
			}
			files.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(name, "assets/") || path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r, index)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, index []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", noCache)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(index)
}
