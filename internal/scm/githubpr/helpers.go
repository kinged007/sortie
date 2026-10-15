package githubpr

import (
	"strconv"
	"strings"

	"github.com/sortie-ai/sortie/internal/httpkit"
)

// defaultEndpoint is the public GitHub REST API base URL, applied when
// the adapter config omits "endpoint".
const defaultEndpoint = "https://api.github.com"

func resolveEndpoint(raw string) (endpoint, redacted string, ok bool) {
	parsed, ok := httpkit.ResolveEndpoint(raw, defaultEndpoint)
	if !ok {
		return "", parsed.Redacted, false
	}
	return parsed.Base, parsed.Redacted, true
}

func containsSlash(s string) bool {
	return strings.Contains(s, "/")
}

func intToStr(n int64) string {
	return strconv.FormatInt(n, 10)
}
