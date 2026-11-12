package notifyhttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const secretPath = "/bot123456:SECRET-TOKEN/sendMessage"

func closedServerURL(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()
	return srv.URL
}

func TestClient_PostJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		status       int
		baseURL      func(t *testing.T) string
		wantCategory string
		isTransport  bool
	}{
		{name: "200 succeeds", status: http.StatusOK},
		{name: "204 succeeds", status: http.StatusNoContent},
		{name: "401 unauthorized", status: http.StatusUnauthorized, wantCategory: "unauthorized (HTTP 401)"},
		{name: "403 unauthorized", status: http.StatusForbidden, wantCategory: "unauthorized (HTTP 403)"},
		{name: "429 rate limited", status: http.StatusTooManyRequests, wantCategory: "rate limited (HTTP 429)"},
		{name: "500 server error", status: http.StatusInternalServerError, wantCategory: "server error (HTTP 500)"},
		{name: "404 unexpected response", status: http.StatusNotFound, wantCategory: "unexpected response (HTTP 404)"},
		{name: "closed server", baseURL: closedServerURL, wantCategory: "connection failure", isTransport: true},
		{name: "unparsable base URL", baseURL: func(*testing.T) string { return "http://127.0.0.1\x7f" }, wantCategory: "connection failure", isTransport: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tt.status) }))
			t.Cleanup(srv.Close)
			endpoint := srv.URL + secretPath
			if tt.baseURL != nil {
				endpoint = tt.baseURL(t) + secretPath
			}

			err := NewClient(endpoint).PostJSON(context.Background(), map[string]string{"text": "hi"})

			var se *sendError
			if tt.wantCategory == "" && err != nil {
				t.Fatalf("PostJSON(status %d) = %v, want nil", tt.status, err)
			}
			if tt.wantCategory != "" && (!errors.As(err, &se) || se.Category != tt.wantCategory) {
				t.Fatalf("PostJSON(%q) = %v, want sendError with category %q", endpoint, err, tt.wantCategory)
			}
			for link := err; tt.isTransport && link != nil; link = errors.Unwrap(link) {
				if _, isURLError := link.(*url.Error); isURLError || strings.Contains(link.Error(), "SECRET-TOKEN") {
					t.Errorf("PostJSON(%q) chain link %T = %q, want no *url.Error and no request URL", endpoint, link, link)
				}
			}
		})
	}
}

func TestClassifyTransport_KeepsDeadlineExceeded(t *testing.T) {
	t.Parallel()

	err := classifyTransport(&url.Error{Op: "Post", URL: "http://x", Err: context.DeadlineExceeded}, "POST", "")

	var se *sendError
	if !errors.As(err, &se) || se.Category != "timeout" || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("classifyTransport(deadline exceeded) = %v, want a timeout sendError matching context.DeadlineExceeded", err)
	}
}
