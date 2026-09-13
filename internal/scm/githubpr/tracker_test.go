package githubpr

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
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

func TestFetchCandidates_RespectsAssigneeFilter(t *testing.T) {
	t.Parallel()

	pr := func(number int, login string) string {
		assignees := `[]`
		if login != "" {
			assignees = `[{"login":` + strconv.Quote(login) + `}]`
		}
		return fmt.Sprintf(`{"number":%d,"title":"PR %d","body":null,"state":"open","html_url":"https://github.com/owner/repo/pull/%d","labels":[{"name":"agent:needs-review"}],"assignees":%s,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
			number, number, number, assignees)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `[`+pr(1, "alice")+`,`+pr(2, "bob")+`,`+pr(3, "")+`]`)
		case "/user":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"login":"alice"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"unexpected path"}`)
		}
	}))
	defer srv.Close()

	cfg := validConfig(srv.URL)
	cfg["active_states"] = []string{"agent:needs-review"}
	cfg["query_filter"] = "assignee:@me"
	a := mustAdapter(t, cfg)
	issues, err := a.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatalf("FetchCandidateIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "1" {
		t.Fatalf("issues = %+v, want only PR 1", issues)
	}

	cfg["query_filter"] = "assignee:bob"
	a = mustAdapter(t, cfg)
	issues, err = a.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatalf("FetchCandidateIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "2" {
		t.Fatalf("issues = %+v, want only PR 2", issues)
	}
}

func TestFetchCandidates_SkipsExcludedLabels(t *testing.T) {
	t.Parallel()

	pr := func(number int, labels string) string {
		return fmt.Sprintf(`{"number":%d,"title":"PR %d","body":null,"state":"open","html_url":"https://github.com/owner/repo/pull/%d","labels":[%s],"assignees":[],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
			number, number, number, labels)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `[`+pr(1, `{"name":"agent:needs-review"}`)+`,`+pr(2, `{"name":"agent:needs-review"},{"name":"needs-human"}`)+`]`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"unexpected path"}`)
		}
	}))
	defer srv.Close()

	cfg := validConfig(srv.URL)
	cfg["active_states"] = []string{"agent:needs-review"}
	cfg["query_filter"] = "-label:needs-human"
	a := mustAdapter(t, cfg)
	issues, err := a.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatalf("FetchCandidateIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "1" {
		t.Fatalf("issues = %+v, want only PR 1", issues)
	}

	issues, err = a.FetchIssuesByStates(context.Background(), []string{"agent:needs-review"})
	if err != nil {
		t.Fatalf("FetchIssuesByStates: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "1" {
		t.Fatalf("issues = %+v, want only PR 1", issues)
	}
}
