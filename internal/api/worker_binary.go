package api

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const defaultWorkerToken = "dev-worker-token"

type workerTokenInfoView struct {
	EnvVar       string `json:"envVar"`
	Header       string `json:"header"`
	IsDefault    bool   `json:"isDefault"`
	DefaultValue string `json:"defaultValue,omitempty"`
}

type workerBinaryInfoView struct {
	Available bool   `json:"available"`
	Filename  string `json:"filename"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256,omitempty"`
	Message   string `json:"message,omitempty"`
}

type workerAccessView struct {
	GatewayURL  string               `json:"gatewayUrl"`
	WorkerToken workerTokenInfoView  `json:"workerToken"`
	Binary      workerBinaryInfoView `json:"binary"`
}

func workerBinaryFilename() string {
	if runtime.GOOS == "windows" {
		return "sched.exe"
	}
	return "sched"
}

func inspectWorkerBinary() (workerBinaryInfoView, error) {
	info := workerBinaryInfoView{Filename: workerBinaryFilename(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	exe, err := os.Executable()
	if err != nil {
		return info, err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return info, err
	}
	file, err := os.Open(exe)
	if err != nil {
		return info, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return info, err
	}
	if !stat.Mode().IsRegular() {
		return info, fmt.Errorf("executable is not a regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return info, err
	}
	info.Available = true
	info.SizeBytes = stat.Size()
	info.SHA256 = fmt.Sprintf("%x", hash.Sum(nil))
	return info, nil
}

// handleWorkerAccess exposes the non-secret bootstrap facts needed by the
// console: gateway address, whether the deployment still uses the public dev
// Worker token, and the exact binary that can be downloaded. Custom tokens are
// deliberately never returned.
func (s *Server) handleWorkerAccess(w http.ResponseWriter, _ *http.Request) {
	token := workerTokenInfoView{EnvVar: "SCHED_WORKER_TOKEN", Header: "X-Worker-Token"}
	if s.cfg.WorkerToken == defaultWorkerToken {
		token.IsDefault = true
		token.DefaultValue = defaultWorkerToken
	}
	binary, err := inspectWorkerBinary()
	if err != nil {
		binary = workerBinaryInfoView{
			Available: false, Filename: workerBinaryFilename(),
			GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
			Message: "服务端未暴露可下载的可执行文件，请从源码构建。",
		}
		s.log.Warn("inspect worker binary", "error", err)
	}
	writeJSON(w, http.StatusOK, workerAccessView{
		GatewayURL:  strings.TrimRight(s.cfg.GatewayURL, "/"),
		WorkerToken: token,
		Binary:      binary,
	})
}

// handleWorkerBinaryDownload serves the running server executable. This is the
// same multi-command binary used for `sched worker`, so the operator does not
// need a separate Worker-only artifact.
func (s *Server) handleWorkerBinaryDownload(w http.ResponseWriter, r *http.Request) {
	exe, err := os.Executable()
	if err != nil {
		s.log.Warn("resolve worker binary", "error", err)
		writeError(w, http.StatusInternalServerError, "worker_binary_unavailable", "worker binary is unavailable")
		return
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		s.log.Warn("resolve worker binary symlink", "error", err)
		writeError(w, http.StatusInternalServerError, "worker_binary_unavailable", "worker binary is unavailable")
		return
	}
	file, err := os.Open(exe)
	if err != nil {
		s.log.Warn("open worker binary", "error", err)
		writeError(w, http.StatusInternalServerError, "worker_binary_unavailable", "worker binary is unavailable")
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		writeError(w, http.StatusInternalServerError, "worker_binary_unavailable", "worker binary is unavailable")
		return
	}
	filename := workerBinaryFilename()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, filename, stat.ModTime(), file)
}
