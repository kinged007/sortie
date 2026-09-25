package githubpr

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/adaptertest"
	"github.com/sortie-ai/sortie/internal/domain"
)

func validConfig(endpoint string) map[string]any {
	return map[string]any{
		"endpoint": endpoint,
		"api_key":  "test-token",
		"project":  "owner/repo",
	}
}

func mustAdapter(t *testing.T, config map[string]any) *GitHubPRAdapter {
	t.Helper()
	a, err := NewGitHubPRAdapter(config)
	if err != nil {
		t.Fatalf("NewGitHubPRAdapter: %v", err)
	}
	return a.(*GitHubPRAdapter)
}

func prJSON(number int, stateLabel, nativeState string) string {
	return fmt.Sprintf(`{"number":%d,"title":"PR %d","body":null,"state":%q,"html_url":"https://github.com/owner/repo/pull/%d","labels":[{"name":%q}],"assignees":[],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
		number, number, nativeState, number, stateLabel)
}

func TestFetchCandidates_ListsOpenPRs(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/repos/owner/repo/pulls" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"unexpected path"}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `[`+prJSON(1, "backlog", "open")+`,`+prJSON(2, "review", "open")+`]`)
	}))
	defer srv.Close()

	a := mustAdapter(t, validConfig(srv.URL))
	issues, err := a.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatalf("FetchCandidateIssues: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("len(issues) = %d, want 2", len(issues))
	}
	if issues[0].ID != "1" || issues[0].State != "backlog" {
		t.Errorf("issues[0] = %+v, want ID 1 state backlog", issues[0])
	}
}

func TestFetchIssueByID_ReadsPullRoute(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/7":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, prJSON(7, "review", "open"))
		case "/repos/owner/repo/issues/7/comments":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `[]`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"unexpected path"}`)
		}
	}))
	defer srv.Close()

	a := mustAdapter(t, validConfig(srv.URL))
	issue, err := a.FetchIssueByID(context.Background(), "7")
	if err != nil {
		t.Fatalf("FetchIssueByID: %v", err)
	}
	if issue.ID != "7" || issue.State != "review" {
		t.Errorf("issue = %+v, want ID 7 state review", issue)
	}
	if issue.Comments == nil {
		t.Error("issue.Comments is nil, want non-nil")
	}
}

func TestTransitionIssue_SwapsLabel(t *testing.T) {
	t.Parallel()

	var deleted, posted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/owner/repo/pulls/7":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, prJSON(7, "backlog", "open"))
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/7/labels/"):
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "POST" && r.URL.Path == "/repos/owner/repo/issues/7/labels":
			posted = append(posted, r.URL.Path)
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `[]`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"unexpected path "+r.URL.Path}`)
		}
	}))
	defer srv.Close()

	a := mustAdapter(t, validConfig(srv.URL))
	if err := a.TransitionIssue(context.Background(), "7", "review"); err != nil {
		t.Fatalf("TransitionIssue: %v", err)
	}
	if len(deleted) != 1 || len(posted) != 1 {
		t.Errorf("deleted=%v posted=%v, want one of each", deleted, posted)
	}
}

func TestFetchCandidates_QueryFilterGoesToSearch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		filter string
		want   string
	}{
		{"assignee", "assignee:bob", "repo:owner/repo type:pr state:open assignee:bob"},
		{"at me is passed through", "assignee:@me", "repo:owner/repo type:pr state:open assignee:@me"},
		{"quoted multi-word label", `label:"needs review"`, `repo:owner/repo type:pr state:open label:"needs review"`},
		{"negative label", "-label:needs-human", "repo:owner/repo type:pr state:open -label:needs-human"},
		{"label OR list", "label:quick,agent:build", "repo:owner/repo type:pr state:open label:quick,agent:build"},
		{"qualifiers the old local parser dropped", "draft:false author:alice", "repo:owner/repo type:pr state:open draft:false author:alice"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotQ string
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				gotPath, gotQ = r.URL.Path, r.URL.Query().Get("q")
				_, _ = io.WriteString(w, `{"total_count":0,"incomplete_results":false,"items":[]}`)
			}))
			defer srv.Close()

			cfg := validConfig(srv.URL)
			cfg["query_filter"] = tt.filter
			a := mustAdapter(t, cfg)
			if _, err := a.FetchCandidateIssues(context.Background()); err != nil {
				t.Fatalf("FetchCandidateIssues: %v", err)
			}

			if gotPath != "/search/issues" {
				t.Errorf("path = %q, want /search/issues", gotPath)
			}
			if gotQ != tt.want {
				t.Errorf("q = %q, want %q", gotQ, tt.want)
			}
		})
	}
}

func TestFetchCandidates_EmptyFilterUsesPullsList(t *testing.T) {
	t.Parallel()

	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/repos/owner/repo/pulls" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `[`+prJSON(1, "backlog", "open")+`]`)
	}))
	defer srv.Close()

	a := mustAdapter(t, validConfig(srv.URL))
	issues, err := a.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatalf("FetchCandidateIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "1" {
		t.Fatalf("issues = %+v, want only PR 1", issues)
	}
	if len(paths) != 1 || paths[0] != "/repos/owner/repo/pulls" {
		t.Errorf("paths = %v, want only the pulls list", paths)
	}
}

func TestFetchIssuesByStates_IgnoresQueryFilter(t *testing.T) {
	t.Parallel()

	var gotQ string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/search/issues" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotQ = r.URL.Query().Get("q")
		_, _ = io.WriteString(w, `{"total_count":1,"incomplete_results":false,"items":[`+
			prJSON(9, "done", "closed")+`]}`)
	}))
	defer srv.Close()

	cfg := validConfig(srv.URL)
	cfg["query_filter"] = "assignee:alice"
	a := mustAdapter(t, cfg)

	issues, err := a.FetchIssuesByStates(context.Background(), []string{"done"})
	if err != nil {
		t.Fatalf("FetchIssuesByStates: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "9" {
		t.Fatalf("issues = %+v, want only PR 9", issues)
	}
	// A filter that stopped matching a dispatched PR would otherwise hide
	// it from reconciliation and strand its workspace.
	if want := `repo:owner/repo type:pr state:closed label:"done"`; gotQ != want {
		t.Errorf("q = %q, want %q", gotQ, want)
	}
}

func TestCandidateBlockersAreAuthoritative(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[`+prJSON(1, "backlog", "open")+`]`)
	}))
	defer srv.Close()

	a := mustAdapter(t, validConfig(srv.URL))
	issues, err := a.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatalf("FetchCandidateIssues: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %+v, want exactly one", issues)
	}
	issue := issues[0]
	if issue.BlockersUnresolved {
		t.Error("BlockersUnresolved = true, want false: pull requests carry no dependency relation")
	}
	if issue.BlockedBy == nil {
		t.Error("BlockedBy is nil, want a non-nil empty slice")
	}
	if len(issue.BlockedBy) != 0 {
		t.Errorf("BlockedBy = %+v, want empty", issue.BlockedBy)
	}
}

func TestAdapterDoesNotImplementBlockerReader(t *testing.T) {
	t.Parallel()

	a := mustAdapter(t, validConfig("https://api.github.com"))
	if _, ok := any(a).(domain.BlockerReader); ok {
		t.Error("adapter implements domain.BlockerReader, want unsupported blocker source to need no reader")
	}
}

func TestErrorClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   int
		headers  map[string]string
		body     string
		wantKind domain.TrackerErrorKind
	}{
		{"403 rate limited is not an auth failure", http.StatusForbidden, map[string]string{"X-Ratelimit-Remaining": "0"}, "", domain.ErrTrackerAPI},
		{"403 permission denied is an auth failure", http.StatusForbidden, nil, "forbidden", domain.ErrTrackerAuth},
		{"422 is a payload error", http.StatusUnprocessableEntity, nil, "cannot reopen a merged pull request", domain.ErrTrackerPayload},
		{"400 is a payload error", http.StatusBadRequest, nil, "bad input", domain.ErrTrackerPayload},
		{"500 is a transport error", http.StatusInternalServerError, nil, "boom", domain.ErrTrackerTransport},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for key, value := range tt.headers {
					w.Header().Set(key, value)
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			a := mustAdapter(t, validConfig(srv.URL))
			_, err := a.FetchCandidateIssues(context.Background())
			adaptertest.AssertTrackerErrorKind(t, err, tt.wantKind)
		})
	}
}
