// Package githubapi holds the HTTP transport shared by the GitHub tracker,
// CI, and SCM adapters and by the GitHub pull-request tracker. It registers no
// kind and holds no adapter; it exists so one timeout, one set of request
// headers, and one HTTP-status-to-error mapping serve every GitHub surface.
package githubapi

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/httpkit"
)

// requestTimeout bounds every GitHub request. The orchestrator runs its poll
// tick inline on the event loop with no per-tick deadline, so an unbounded
// client can wedge polling, worker-exit handling, and retry timers together.
// It is a var so the timeout can be lowered in tests.
var requestTimeout = 30 * time.Second

// NewClient returns a client for the GitHub REST API at baseURL,
// authenticated with token as a bearer credential.
func NewClient(baseURL, token, userAgent string) *httpkit.Client {
	trimmedBaseURL := strings.TrimRight(baseURL, "/")
	authorization := "Bearer " + token

	return httpkit.NewClient(httpkit.ClientOptions{
		BaseURL: trimmedBaseURL,
		Timeout: requestTimeout,
		Authorize: func(req *http.Request) {
			req.Header.Set("Authorization", authorization)
			req.Header.Set("Accept", "application/vnd.github+json")
			req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
			req.Header.Set("User-Agent", userAgent)
		},
		ClassifyError:     classifyHTTPError,
		ClassifyTransport: httpkit.ClassifyTransport,
	})
}

const maxErrorBody = 512

// classifyHTTPError maps a non-success GitHub response to a domain tracker
// error. A 403 carries either an exhausted rate limit or a permission
// failure, so the remaining-requests header is read before the body: the two
// map to different kinds and the orchestrator retries them differently.
func classifyHTTPError(resp *http.Response, method, path string) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	_, _ = io.Copy(io.Discard, resp.Body)
	detail := string(snippet)

	switch {
	case resp.StatusCode == http.StatusBadRequest:
		return &domain.TrackerError{
			Kind:    domain.ErrTrackerPayload,
			Message: fmt.Sprintf("%s %s: bad request: %s", method, path, detail),
			Status:  resp.StatusCode,
		}

	case resp.StatusCode == http.StatusUnauthorized:
		return &domain.TrackerError{
			Kind:    domain.ErrTrackerAuth,
			Message: fmt.Sprintf("%s %s: bad credentials", method, path),
			Status:  resp.StatusCode,
		}

	case resp.StatusCode == http.StatusForbidden:
		if resp.Header.Get("X-Ratelimit-Remaining") == "0" {
			return &domain.TrackerError{
				Kind:    domain.ErrTrackerAPI,
				Message: fmt.Sprintf("%s %s: rate limited (primary)", method, path),
				Status:  resp.StatusCode,
			}
		}
		if strings.Contains(strings.ToLower(detail), "rate limit") {
			message := fmt.Sprintf("%s %s: rate limited (secondary)", method, path)
			if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
				message += fmt.Sprintf(" (retry after %s seconds)", retryAfter)
			}
			return &domain.TrackerError{
				Kind:    domain.ErrTrackerAPI,
				Message: message,
				Status:  resp.StatusCode,
			}
		}
		return &domain.TrackerError{
			Kind:    domain.ErrTrackerAuth,
			Message: fmt.Sprintf("%s %s: insufficient permissions", method, path),
			Status:  resp.StatusCode,
		}

	case resp.StatusCode == http.StatusNotFound:
		return &domain.TrackerError{
			Kind:    domain.ErrTrackerNotFound,
			Message: fmt.Sprintf("%s %s: not found", method, path),
			Status:  resp.StatusCode,
		}

	case resp.StatusCode == http.StatusGone:
		return &domain.TrackerError{
			Kind:    domain.ErrTrackerAPI,
			Message: fmt.Sprintf("%s %s: gone (410): %s", method, path, detail),
			Status:  resp.StatusCode,
		}

	case resp.StatusCode == http.StatusMethodNotAllowed:
		return &domain.TrackerError{
			Kind:    domain.ErrTrackerAPI,
			Message: fmt.Sprintf("%s %s: method not allowed: %s", method, path, detail),
			Status:  resp.StatusCode,
		}

	case resp.StatusCode == http.StatusConflict:
		return &domain.TrackerError{
			Kind:    domain.ErrTrackerAPI,
			Message: fmt.Sprintf("%s %s: conflict: %s", method, path, detail),
			Status:  resp.StatusCode,
		}

	case resp.StatusCode == http.StatusUnprocessableEntity:
		return &domain.TrackerError{
			Kind:    domain.ErrTrackerPayload,
			Message: fmt.Sprintf("%s %s: validation failed: %s", method, path, detail),
			Status:  resp.StatusCode,
		}

	case resp.StatusCode == http.StatusTooManyRequests:
		message := fmt.Sprintf("%s %s: rate limited", method, path)
		if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
			message += fmt.Sprintf(" (retry after %s seconds)", retryAfter)
		}
		return &domain.TrackerError{
			Kind:    domain.ErrTrackerAPI,
			Message: message,
			Status:  resp.StatusCode,
		}

	case resp.StatusCode >= 500:
		return &domain.TrackerError{
			Kind:    domain.ErrTrackerTransport,
			Message: fmt.Sprintf("%s %s: server error %d: %s", method, path, resp.StatusCode, detail),
			Status:  resp.StatusCode,
		}

	default:
		return &domain.TrackerError{
			Kind:    domain.ErrTrackerAPI,
			Message: fmt.Sprintf("%s %s: unexpected status %d: %s", method, path, resp.StatusCode, detail),
			Status:  resp.StatusCode,
		}
	}
}
