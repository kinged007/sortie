package notifyhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
)

// sendError is a classified notifier failure. It carries a category
// (timeout, connection failure, an HTTP status class) and deliberately
// omits the endpoint URL, the request body, and the response body so
// the secret-bearing URL never reaches a log or an agent-visible result.
type sendError struct {
	Category string
	Err      error
}

// Error returns the category only.
func (e *sendError) Error() string { return e.Category }

// Unwrap exposes the underlying transport error for [errors.Is] and
// [errors.As] without surfacing its text into the category.
func (e *sendError) Unwrap() error { return e.Err }

// classifyHTTPError maps a non-2xx response to a [sendError] whose
// category is the HTTP status class. The response body is drained and
// discarded, never read into the error.
func classifyHTTPError(resp *http.Response, _, _ string) error {
	_, _ = io.Copy(io.Discard, resp.Body)

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &sendError{Category: fmt.Sprintf("unauthorized (HTTP %d)", resp.StatusCode)}
	case resp.StatusCode == http.StatusTooManyRequests:
		return &sendError{Category: "rate limited (HTTP 429)"}
	case resp.StatusCode >= 500:
		return &sendError{Category: fmt.Sprintf("server error (HTTP %d)", resp.StatusCode)}
	default:
		return &sendError{Category: fmt.Sprintf("unexpected response (HTTP %d)", resp.StatusCode)}
	}
}

// classifyTransport maps a request, network, or body-read failure to a
// [sendError] with a transport category. The category never renders the
// error text. The chain keeps the cause beneath a [*url.Error], because
// that wrapper holds the full request URL and the URL carries the secret.
func classifyTransport(err error, _, _ string) error {
	category := "connection failure"
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		category = "timeout"
	}

	cause := err
	if urlErr, ok := errors.AsType[*url.Error](err); ok {
		cause = urlErr.Err
	}
	return &sendError{Category: category, Err: cause}
}
