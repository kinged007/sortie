// Package notifyhttp provides the JSON-over-HTTP transport shared by the
// notifier kinds that post to an operator-configured endpoint. It owns
// the per-call timeout and the classification of transport and non-2xx
// failures, so no kind repeats them and no classified error carries the
// endpoint URL, the request body, or the response body.
//
// The package registers no notifier kind. Start with [NewClient].
package notifyhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sortie-ai/sortie/internal/httpkit"
)

// sendTimeout bounds each outbound POST so a slow endpoint cannot stall
// the agent turn.
const sendTimeout = 10 * time.Second

// Client posts JSON to one endpoint. It is immutable after construction,
// so PostJSON is safe for concurrent use.
type Client struct {
	http *httpkit.Client
}

// NewClient returns a Client that posts to endpoint. The endpoint may
// carry a secret, so it appears in no error the client returns.
func NewClient(endpoint string) *Client {
	return &Client{http: httpkit.NewClient(httpkit.ClientOptions{
		BaseURL:           endpoint,
		Timeout:           sendTimeout,
		ClassifyError:     classifyHTTPError,
		ClassifyTransport: classifyTransport,
	})}
}

// PostJSON encodes payload as JSON, posts it to the endpoint, and returns
// nil on any 2xx response. A non-2xx response or a transport failure yields
// a classified error, and a done ctx yields ctx.Err().
func (c *Client) PostJSON(ctx context.Context, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}

	if _, err := c.http.Send(ctx, http.MethodPost, "", bytes.NewReader(encoded)); err != nil {
		return err
	}
	return nil
}
