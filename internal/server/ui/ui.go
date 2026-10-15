// Package ui serves the dashboard application compiled into the binary.
package ui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// The all: prefix keeps files whose names begin with an underscore or a
// dot, which the bundler emits and the toolchain would otherwise drop.
//
//go:embed all:dist
var distFS embed.FS

const (
	assetsPrefix = "/ui/assets/"

	// contentSecurityPolicy forbids inline script and style, so the
	// document and everything it loads must come from the same origin.
	contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

	immutableCacheControl = "public, max-age=31536000, immutable"
)

// assetContentTypes is fixed because mime.TypeByExtension consults the
// host's registry on Windows, which can serve a script or stylesheet under
// a type the browser refuses.
var assetContentTypes = map[string]string{
	".js":  "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8",
}

// Handler returns the handler for GET and HEAD requests whose path begins
// with /ui/. It panics when the embedded build output has no index.html.
// The handler is immutable and safe for concurrent use.
func Handler() http.Handler {
	dist, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(fmt.Sprintf("ui: open embedded output: %v", err))
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		panic(fmt.Sprintf("ui: embedded output has no index.html: %v", err))
	}
	sum := sha256.Sum256(index)
	return &handler{
		dist:  dist,
		index: index,
		etag:  `"` + hex.EncodeToString(sum[:]) + `"`,
	}
}

type handler struct {
	dist  fs.FS
	index []byte
	etag  string
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if name, ok := strings.CutPrefix(r.URL.Path, assetsPrefix); ok {
		h.serveAsset(w, r, name)
		return
	}
	h.serveDocument(w, r)
}

func (h *handler) serveDocument(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Cache-Control", "no-cache")
	header.Set("ETag", h.etag)
	setSecurityHeaders(header)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(h.index))
}

func (h *handler) serveAsset(w http.ResponseWriter, r *http.Request, name string) {
	contentType, ok := assetContentTypes[path.Ext(name)]
	if !ok || !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	filePath := "assets/" + name
	info, err := fs.Stat(h.dist, filePath)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	body, err := fs.ReadFile(h.dist, filePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Cache-Control", immutableCacheControl)
	setSecurityHeaders(header)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}

func setSecurityHeaders(header http.Header) {
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", contentSecurityPolicy)
}
