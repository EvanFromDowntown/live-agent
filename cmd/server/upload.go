package main

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxUploadBytes = 20 << 20 // 20 MiB per file
	maxUploadTotal = 60 << 20 // 60 MiB per request
)

// isImageExt reports whether a filename looks like a raster image we can show
// inline / send to a vision model.
func isImageExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return true
	}
	return false
}

// mimeOf returns a best-effort content type for a filename.
func mimeOf(name string) string {
	if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// uniqueName returns a filename that does not yet exist in dir, appending
// -1, -2, ... before the extension when needed.
func uniqueName(dir, name string) string {
	if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if _, err := os.Stat(filepath.Join(dir, cand)); os.IsNotExist(err) {
			return cand
		}
	}
}

// sanitizeName strips any path and control characters from an uploaded filename.
func sanitizeName(name string) string {
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == '/' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		name = fmt.Sprintf("file-%d", time.Now().UnixNano())
	}
	return name
}

// handleUpload accepts multipart file uploads for a session and stores them
// under <workspace>/<session>/uploads/. A new session id is minted when none is
// supplied, so a user can attach files before the conversation has started; the
// client then uses the returned session id for the /stream turn.
func (s *srv) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	session := strings.TrimSpace(r.URL.Query().Get("session"))
	if session == "" {
		session = fmt.Sprintf("sess_%d", time.Now().UnixNano())
	}
	session = filepath.Base(session)
	dir := filepath.Join(s.wsBase, session, "uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, "mkdir failed", http.StatusInternalServerError)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadTotal+(1<<20))
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		http.Error(w, "upload too large or malformed", http.StatusBadRequest)
		return
	}
	fhs := r.MultipartForm.File["files"]
	if len(fhs) == 0 {
		http.Error(w, "no files", http.StatusBadRequest)
		return
	}

	type uploaded struct {
		Name  string `json:"name"`  // relative to the workspace root
		Base  string `json:"base"`  // just the filename
		Size  int64  `json:"size"`
		Mime  string `json:"mime"`
		Image bool   `json:"image"`
	}
	var out []uploaded
	for _, fh := range fhs {
		if fh.Size > maxUploadBytes {
			http.Error(w, fmt.Sprintf("%q exceeds %d MB limit", fh.Filename, maxUploadBytes>>20), http.StatusRequestEntityTooLarge)
			return
		}
		name := uniqueName(dir, sanitizeName(fh.Filename))
		src, err := fh.Open()
		if err != nil {
			http.Error(w, "cannot read upload", http.StatusBadRequest)
			return
		}
		dst, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			src.Close()
			http.Error(w, "cannot save upload", http.StatusInternalServerError)
			return
		}
		n, cErr := io.Copy(dst, src)
		src.Close()
		dst.Close()
		if cErr != nil {
			http.Error(w, "write failed", http.StatusInternalServerError)
			return
		}
		out = append(out, uploaded{
			Name:  "uploads/" + name,
			Base:  name,
			Size:  n,
			Mime:  mimeOf(name),
			Image: isImageExt(name),
		})
	}
	writeJSON(w, map[string]any{"session": session, "files": out})
}
