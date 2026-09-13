package githubpr

import (
	"net/http"
	"strings"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/httpkit"
	"github.com/sortie-ai/sortie/internal/issuekit"
)

// pullRequest is the subset of the Pulls REST API payload this adapter
// reads: identity, state derivation inputs, and prompt fields.
type pullRequest struct {
	Number    int         `json:"number"`
	Title     string      `json:"title"`
	Body      *string     `json:"body"`
	State     string      `json:"state"`
	HTMLURL   string      `json:"html_url"`
	Labels    []pullLabel `json:"labels"`
	Assignees []pullUser  `json:"assignees"`
	CreatedAt string      `json:"created_at"`
	UpdatedAt string      `json:"updated_at"`
}

type pullLabel struct {
	Name string `json:"name"`
}

type pullUser struct {
	Login string `json:"login"`
}

type pullComment struct {
	ID        int64    `json:"id"`
	User      pullUser `json:"user"`
	Body      string   `json:"body"`
	CreatedAt string   `json:"created_at"`
}

// searchResponse is GET /search/issues restricted to type:pr items.
type searchResponse struct {
	TotalCount int          `json:"total_count"`
	Items      []searchItem `json:"items"`
}

// searchItem carries the pulls-listed shape nested under pull_request
// plus the comment-body fields FetchIssueByID needs.
type searchItem struct {
	pullRequest
	PullRef *struct{} `json:"pull_request"`
}

func (s searchItem) asPullRequest() pullRequest {
	return s.pullRequest
}

func labelNames(labels []pullLabel) []string {
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, l.Name)
	}
	return names
}

func normalizeComments(raw []pullComment) []domain.Comment {
	source := make([]issuekit.SourceComment, len(raw))
	for i, c := range raw {
		source[i] = issuekit.SourceComment{
			ID:        intToStr(c.ID),
			Author:    c.User.Login,
			Body:      c.Body,
			CreatedAt: c.CreatedAt,
		}
	}
	return issuekit.NormalizeComments(source)
}

func newClient(baseURL, token, userAgent string) *httpkit.Client {
	trimmed := strings.TrimRight(baseURL, "/")
	authorization := "Bearer " + token
	return httpkit.NewClient(httpkit.ClientOptions{
		BaseURL: trimmed,
		Authorize: func(req *http.Request) {
			req.Header.Set("Authorization", authorization)
			req.Header.Set("Accept", "application/vnd.github+json")
			req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
			req.Header.Set("User-Agent", userAgent)
		},
		ClassifyError:     classifyError,
		ClassifyTransport: httpkit.ClassifyTransport,
	})
}
