package server

import (
	_ "embed" // required for //go:embed directives
	"net/http"
)

//go:embed favicon.ico
var faviconICO []byte

// handleFavicon serves the embedded favicon.ico with aggressive caching.
func handleFavicon(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(faviconICO)
}
