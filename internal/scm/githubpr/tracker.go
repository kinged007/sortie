// Package githubpr registers the "github-pr" tracker kind: GitHub pull
// requests surfaced as Sortie issues. Reads go to the Pulls REST API
// (which returns PRs the issues-route adapter skips); label writes go
// to the Issues routes (which accept PR numbers as issue numbers).
package githubpr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/httpkit"
	"github.com/sortie-ai/sortie/internal/issuekit"
	"github.com/sortie-ai/sortie/internal/registry"
	"github.com/sortie-ai/sortie/internal/scm/githubapi"
	"github.com/sortie-ai/sortie/internal/trackermetrics"
	"github.com/sortie-ai/sortie/internal/typeutil"
)

func init() {
	registry.Trackers.RegisterWithMeta("github-pr", NewGitHubPRAdapter, registry.TrackerMeta{
		RequiresProject:        true,
		RequiresAPIKey:         true,
		RequiresTerminalStates: true,
		ValidateTrackerConfig:  validateConfig,
		BlockerSource:          registry.BlockersUnsupported,
	})
}

var _ domain.TrackerAdapter = (*GitHubPRAdapter)(nil)

const maxPages = 200

// GitHubPRAdapter implements [domain.TrackerAdapter] against the GitHub
// Pulls REST API for reads and the Issues REST API for label writes.
// Safe for concurrent use.
type GitHubPRAdapter struct {
	*issuekit.StateSource
	client      *httpkit.Client
	owner       string
	repo        string
	queryFilter string
	metrics     domain.Metrics
	log         *slog.Logger
	model       *issuekit.LabelStateModel
}

// NewGitHubPRAdapter creates a [GitHubPRAdapter] from adapter
// configuration. Required config keys: "api_key", "project"
// (owner/repo format). Optional: "endpoint", "active_states",
// "terminal_states", "handoff_state", "in_progress_state", "query_filter", "user_agent".
func NewGitHubPRAdapter(config map[string]any) (domain.TrackerAdapter, error) {
	apiKey, fault := typeutil.StringField(config, "api_key")
	if fault != nil {
		return nil, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: fault.Error()}
	}
	if apiKey == "" {
		return nil, &domain.TrackerError{Kind: domain.ErrMissingTrackerAPIKey, Message: "missing required config key: api_key"}
	}

	project, fault := typeutil.StringField(config, "project")
	if fault != nil {
		return nil, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: fault.Error()}
	}
	owner, repo, ok := splitProject(project)
	if !ok {
		return nil, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "project must be in owner/repo format"}
	}

	endpointRaw, fault := typeutil.StringField(config, "endpoint")
	if fault != nil {
		return nil, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: fault.Error()}
	}
	endpoint, redacted, endpointOK := resolveEndpoint(endpointRaw)
	if !endpointOK {
		return nil, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: fmt.Sprintf("github-pr: endpoint %q is not a valid absolute http(s) url", redacted)}
	}

	states, err := issuekit.TrackerStatesFromConfig(config)
	if err != nil {
		return nil, err
	}
	source := issuekit.NewStateSource(states)

	queryFilter, fault := typeutil.StringField(config, "query_filter")
	if fault != nil {
		return nil, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: fault.Error()}
	}
	userAgent, fault := typeutil.StringField(config, "user_agent")
	if fault != nil {
		return nil, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: fault.Error()}
	}
	if userAgent == "" {
		userAgent = "sortie/dev"
	}

	return &GitHubPRAdapter{
		StateSource: source,
		client:      githubapi.NewClient(endpoint, apiKey, userAgent),
		owner:       owner,
		repo:        repo,
		queryFilter: queryFilter,
		log:         slog.Default(),
		model:       issuekit.NewLabelStateModel(source, slog.Default()),
	}, nil
}

func splitProject(project string) (owner, repo string, ok bool) {
	o, r, cut := strings.Cut(project, "/")
	if !cut || o == "" || r == "" || containsSlash(r) {
		return "", "", false
	}
	return o, r, true
}

// FetchCandidateIssues returns open pull requests carrying a configured
// active-state label. Comments are nil on all returned issues. When
// query_filter is empty, candidates are listed from the pulls endpoint and
// filtered by label in process. When it is set, the filter is a raw GitHub
// search-qualifier fragment handed to the search endpoint, which applies it
// server-side; an empty filter keeps the plain pulls-list route.
func (a *GitHubPRAdapter) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	states := a.model.Snapshot()
	issues := make([]domain.Issue, 0)
	if len(states.Active) == 0 {
		return issues, nil
	}
	err := trackermetrics.Track(a.metrics, "fetch_candidates", func() error {
		a.model.CheckActiveLabels(ctx, states, func(ctx context.Context) ([]string, error) {
			return a.readRepositoryLabelNames(ctx)
		})
		var fetched []domain.Issue
		var fetchErr error
		if a.queryFilter != "" {
			fetched, fetchErr = a.fetchCandidatesViaSearch(ctx, states)
		} else {
			fetched, fetchErr = a.fetchOpenPRs(ctx, states)
		}
		if fetchErr != nil {
			return fetchErr
		}
		issues = a.model.Candidates(states, fetched)
		for i := range issues {
			issues[i].Comments = nil
		}
		return nil
	})
	return issues, err
}

// fetchCandidatesViaSearch fetches open pull requests through the search
// endpoint so the operator's query_filter is evaluated by GitHub. The filter
// is passed through verbatim: GitHub documents @me as a suffix for every
// username qualifier, so no client-side clause parsing is required.
func (a *GitHubPRAdapter) fetchCandidatesViaSearch(ctx context.Context, states issuekit.LabelStates) ([]domain.Issue, error) {
	q := fmt.Sprintf("repo:%s/%s type:pr state:open %s", a.owner, a.repo, a.queryFilter)
	params := url.Values{
		"q":        {q},
		"sort":     {"created"},
		"order":    {"asc"},
		"per_page": {"50"},
	}

	paginator := httpkit.NewLinkPaginator(a.client, "/search/issues", params, func(body []byte) ([]domain.Issue, error) {
		var page searchResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to parse search response", Err: err}
		}
		if page.IncompleteResults {
			slog.Warn("github search returned incomplete results", slog.Int("total_count", page.TotalCount))
		}
		issues := make([]domain.Issue, 0, len(page.Items))
		for _, item := range page.Items {
			issues = append(issues, a.normalize(item.asPullRequest(), states))
		}
		return issues, nil
	}, httpkit.PaginatorOptions{
		MaxPages: maxPages,
		OnLimitReached: func(limit int) {
			slog.Warn("pagination limit reached",
				slog.Int("max_pages", limit),
				slog.String("endpoint", "/search/issues"))
		},
	})

	open, err := paginator.All(ctx)
	if err != nil {
		return nil, err
	}
	return open, nil
}

func (a *GitHubPRAdapter) fetchOpenPRs(ctx context.Context, states issuekit.LabelStates) ([]domain.Issue, error) {
	path := "/repos/" + a.owner + "/" + a.repo + "/pulls"
	params := url.Values{
		"state":     {"open"},
		"sort":      {"created"},
		"direction": {"asc"},
		"per_page":  {"50"},
	}

	paginator := httpkit.NewLinkPaginator(a.client, path, params, func(body []byte) ([]domain.Issue, error) {
		var raw []pullRequest
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to parse pulls response", Err: err}
		}
		issues := make([]domain.Issue, 0, len(raw))
		for _, pr := range raw {
			issue := a.normalize(pr, states)
			issues = append(issues, issue)
		}
		return issues, nil
	}, httpkit.PaginatorOptions{
		MaxPages: maxPages,
		OnLimitReached: func(limit int) {
			slog.Warn("pagination limit reached",
				slog.Int("max_pages", limit),
				slog.String("endpoint", path))
		},
	})

	return paginator.All(ctx)
}

// FetchIssueByID returns a fully populated pull request including
// comments. The issueID is the PR number as a string.
func (a *GitHubPRAdapter) FetchIssueByID(ctx context.Context, issueID string) (domain.Issue, error) {
	var issue domain.Issue
	err := trackermetrics.Track(a.metrics, "fetch_issue", func() error {
		basePath := "/repos/" + a.owner + "/" + a.repo + "/pulls/" + url.PathEscape(issueID)

		body, _, err := a.client.Get(ctx, basePath, nil)
		if err != nil {
			if domain.IsNotFound(err) {
				return &domain.TrackerError{Kind: domain.ErrTrackerNotFound, Message: fmt.Sprintf("pull request not found: %s", issueID)}
			}
			return err
		}

		var pr pullRequest
		if err := json.Unmarshal(body, &pr); err != nil {
			return &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to parse pull response", Err: err}
		}
		if pr.Number == 0 {
			return &domain.TrackerError{Kind: domain.ErrTrackerNotFound, Message: fmt.Sprintf("pull request not found: %s", issueID)}
		}

		states := a.model.Snapshot()
		issue = a.normalize(pr, states)

		issue.Comments, err = a.FetchIssueComments(ctx, issueID)
		return err
	})
	return issue, err
}

// FetchIssuesByStates returns pull requests in the specified states:
// open PRs via the pulls list endpoint for non-terminal states,
// closed PRs via search for terminal states. The operator's query_filter
// is not applied here: this operation drives running-state reconciliation
// and startup terminal cleanup, and a filter that no longer matches a
// dispatched PR would strand its workspace.
func (a *GitHubPRAdapter) FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error) {
	if len(states) == 0 {
		return []domain.Issue{}, nil
	}
	snapshot := a.model.Snapshot()
	stateSet := make(map[string]struct{}, len(states))
	for _, s := range states {
		stateSet[strings.ToLower(s)] = struct{}{}
	}

	var requestedTerminal []string
	needOpenFetch := false
	for s := range stateSet {
		if slices.Contains(snapshot.Terminal, s) {
			requestedTerminal = append(requestedTerminal, s)
		} else {
			needOpenFetch = true
		}
	}

	var matched []domain.Issue
	err := trackermetrics.Track(a.metrics, "fetch_by_states", func() error {
		matchedIssues := make([]domain.Issue, 0)
		seen := make(map[string]struct{})

		if needOpenFetch {
			prs, err := a.fetchOpenPRs(ctx, snapshot)
			if err != nil {
				return err
			}
			for _, issue := range prs {
				if _, ok := stateSet[issue.State]; !ok {
					continue
				}
				if _, dup := seen[issue.Identifier]; dup {
					continue
				}
				issue.Comments = nil
				matchedIssues = append(matchedIssues, issue)
				seen[issue.Identifier] = struct{}{}
			}
		}

		for _, label := range requestedTerminal {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			issues, err := a.fetchClosedPRsByLabel(ctx, label, seen, snapshot)
			if err != nil {
				return err
			}
			matchedIssues = append(matchedIssues, issues...)
		}

		matched = matchedIssues
		return nil
	})
	return matched, err
}

func (a *GitHubPRAdapter) fetchClosedPRsByLabel(ctx context.Context, label string, seen map[string]struct{}, states issuekit.LabelStates) ([]domain.Issue, error) {
	q := fmt.Sprintf(`repo:%s/%s type:pr state:closed label:"%s"`, a.owner, a.repo, label)
	params := url.Values{
		"q":        {q},
		"sort":     {"created"},
		"order":    {"asc"},
		"per_page": {"50"},
	}

	paginator := httpkit.NewLinkPaginator(a.client, "/search/issues", params, func(body []byte) ([]domain.Issue, error) {
		var page searchResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to parse search response", Err: err}
		}
		if page.IncompleteResults {
			slog.Warn("github search returned incomplete results", slog.String("label", label))
		}
		issues := make([]domain.Issue, 0, len(page.Items))
		for _, item := range page.Items {
			pr := item.asPullRequest()
			issue := a.normalize(pr, states)
			if _, dup := seen[issue.Identifier]; dup {
				continue
			}
			issue.Comments = nil
			issues = append(issues, issue)
			seen[issue.Identifier] = struct{}{}
		}
		return issues, nil
	}, httpkit.PaginatorOptions{
		MaxPages: maxPages,
		OnLimitReached: func(limit int) {
			slog.Warn("pagination limit reached",
				slog.Int("max_pages", limit),
				slog.String("endpoint", "/search/issues"),
				slog.String("label", label))
		},
	})

	return paginator.All(ctx)
}

// FetchIssueStatesByIDs returns the current state for each requested PR
// number. PRs not found are omitted from the map.
func (a *GitHubPRAdapter) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) (map[string]string, error) {
	var states map[string]string
	err := trackermetrics.Track(a.metrics, "fetch_states_by_ids", func() error {
		fetched, err := a.fetchStatesByNumbers(ctx, issueIDs)
		if err != nil {
			return err
		}
		states = fetched
		return nil
	})
	return states, err
}

// FetchIssueStatesByIdentifiers returns the current state for each
// requested identifier. Since ID and Identifier are both the PR number,
// this delegates to [GitHubPRAdapter.fetchStatesByNumbers].
func (a *GitHubPRAdapter) FetchIssueStatesByIdentifiers(ctx context.Context, identifiers []string) (map[string]string, error) {
	var states map[string]string
	err := trackermetrics.Track(a.metrics, "fetch_states_by_identifiers", func() error {
		fetched, err := a.fetchStatesByNumbers(ctx, identifiers)
		if err != nil {
			return err
		}
		states = fetched
		return nil
	})
	return states, err
}

func (a *GitHubPRAdapter) fetchStatesByNumbers(ctx context.Context, numbers []string) (map[string]string, error) {
	if len(numbers) == 0 {
		return map[string]string{}, nil
	}
	snapshot := a.model.Snapshot()
	states := make(map[string]string, len(numbers))
	for _, num := range numbers {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		path := "/repos/" + a.owner + "/" + a.repo + "/pulls/" + url.PathEscape(num)
		body, _, err := a.client.Get(ctx, path, nil)
		if err != nil {
			if domain.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		var pr pullRequest
		if err := json.Unmarshal(body, &pr); err != nil {
			return nil, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to parse pull response", Err: err}
		}
		if pr.Number == 0 {
			continue
		}
		labels := labelNames(pr.Labels)
		states[num] = a.model.Derive(snapshot, num, labels, pr.State == "closed")
	}
	return states, nil
}

// FetchIssueComments returns comments for the specified PR number via
// the issues comments route. Returns an empty non-nil slice when none
// exist.
func (a *GitHubPRAdapter) FetchIssueComments(ctx context.Context, issueID string) ([]domain.Comment, error) {
	comments := make([]domain.Comment, 0)
	err := trackermetrics.Track(a.metrics, "fetch_comments", func() error {
		path := "/repos/" + a.owner + "/" + a.repo + "/issues/" + url.PathEscape(issueID) + "/comments"
		params := url.Values{"per_page": {"50"}}

		paginator := httpkit.NewLinkPaginator(a.client, path, params, func(body []byte) ([]pullComment, error) {
			var raw []pullComment
			if err := json.Unmarshal(body, &raw); err != nil {
				return nil, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to parse comments response", Err: err}
			}
			return raw, nil
		}, httpkit.PaginatorOptions{
			MaxPages: maxPages,
			OnLimitReached: func(limit int) {
				slog.Warn("pagination limit reached",
					slog.Int("max_pages", limit),
					slog.String("endpoint", path))
			},
		})

		raw, err := paginator.All(ctx)
		if err != nil {
			if domain.IsNotFound(err) {
				return &domain.TrackerError{Kind: domain.ErrTrackerNotFound, Message: fmt.Sprintf("pull request not found: %s", issueID)}
			}
			return err
		}
		comments = normalizeComments(raw)
		return nil
	})
	return comments, err
}

// TransitionIssue moves a PR to the target state by swapping the state
// label via the issues labels route and closing or reopening the PR.
func (a *GitHubPRAdapter) TransitionIssue(ctx context.Context, issueID string, targetState string) error {
	return trackermetrics.Track(a.metrics, "transition", func() error {
		prPath := "/repos/" + a.owner + "/" + a.repo + "/pulls/" + url.PathEscape(issueID)
		body, _, err := a.client.Get(ctx, prPath, nil)
		if err != nil {
			return err
		}
		var pr pullRequest
		if err := json.Unmarshal(body, &pr); err != nil {
			return &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to parse pull response", Err: err}
		}

		snapshot := a.model.Snapshot()
		plan, err := snapshot.PlanTransition(targetState, labelNames(pr.Labels), pr.State == "closed")
		if err != nil {
			return err
		}
		return issuekit.ApplyTransition(ctx, plan, issuekit.TransitionOps{
			Labels: a.labelOps(issueID),
			SetClosed: func(ctx context.Context, closed bool) error {
				return a.setIssueClosed(ctx, prPath, closed)
			},
		})
	})
}

func (a *GitHubPRAdapter) setIssueClosed(ctx context.Context, prPath string, closed bool) error {
	var payload []byte
	var err error
	if closed {
		payload, err = json.Marshal(map[string]any{"state": "closed"})
	} else {
		payload, err = json.Marshal(map[string]any{"state": "open"})
	}
	if err != nil {
		return &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to marshal state payload", Err: err}
	}
	_, err = a.client.Send(ctx, "PATCH", prPath, bytes.NewReader(payload))
	return err
}

// CommentIssue posts a Markdown comment on the specified PR via the
// issues comments route.
func (a *GitHubPRAdapter) CommentIssue(ctx context.Context, issueID string, text string) error {
	return trackermetrics.Track(a.metrics, "comment", func() error {
		return a.postComment(ctx, issueID, text)
	})
}

// CommentIssueWithLiteral posts text followed by literal in a fenced Markdown
// code block, so the tracker shows literal verbatim.
func (a *GitHubPRAdapter) CommentIssueWithLiteral(ctx context.Context, issueID, text, literal string) error {
	return trackermetrics.Track(a.metrics, "comment", func() error {
		return a.postComment(ctx, issueID, issuekit.MarkdownLiteralComment(text, literal))
	})
}

func (a *GitHubPRAdapter) postComment(ctx context.Context, issueID, body string) error {
	path := "/repos/" + a.owner + "/" + a.repo + "/issues/" + url.PathEscape(issueID) + "/comments"
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to marshal comment payload", Err: err}
	}
	_, err = a.client.Send(ctx, "POST", path, bytes.NewReader(payload))
	return err
}

// AddLabel adds a label to the specified PR via the issues labels route.
func (a *GitHubPRAdapter) AddLabel(ctx context.Context, issueID string, label string) error {
	return trackermetrics.Track(a.metrics, "add_label", func() error {
		return issuekit.AddIssueLabel(ctx, label, a.labelOps(issueID))
	})
}

// RemoveLabel removes every label of the PR that names label, ignoring
// letter case, and confirms the removal against the PR's labels.
func (a *GitHubPRAdapter) RemoveLabel(ctx context.Context, issueID string, label string) error {
	return trackermetrics.Track(a.metrics, "remove_label", func() error {
		return issuekit.RemoveIssueLabel(ctx, label, a.labelOps(issueID))
	})
}

// SetMetrics configures the metrics recorder for tracker API call
// instrumentation. When not called or called with nil, the adapter
// operates without recording metrics.
func (a *GitHubPRAdapter) SetMetrics(m domain.Metrics) {
	a.metrics = m
}

func (a *GitHubPRAdapter) labelOps(issueID string) issuekit.IssueLabelOps {
	labelsPath := "/repos/" + url.PathEscape(a.owner) + "/" + url.PathEscape(a.repo) + "/issues/" + url.PathEscape(issueID) + "/labels"
	return issuekit.IssueLabelOps{
		Read: func(ctx context.Context) ([]issuekit.IssueLabel, error) {
			labels, err := a.readIssueLabels(ctx, labelsPath)
			if err != nil {
				return nil, err
			}
			stored := make([]issuekit.IssueLabel, len(labels))
			for i, l := range labels {
				stored[i] = issuekit.IssueLabel{Name: l.Name}
			}
			return stored, nil
		},
		Add: func(ctx context.Context, label string) (issuekit.LabelWrite, error) {
			payload, err := json.Marshal(map[string][]string{"labels": {label}})
			if err != nil {
				return issuekit.LabelWrite{}, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to marshal label payload", Err: err}
			}
			body, err := a.client.Send(ctx, "POST", labelsPath, bytes.NewReader(payload))
			if err != nil {
				return issuekit.LabelWrite{}, err
			}
			return reportedLabelList(body), nil
		},
		Remove: func(ctx context.Context, labels []issuekit.IssueLabel) (issuekit.LabelWrite, error) {
			var write issuekit.LabelWrite
			var rejected error
			for _, label := range labels {
				body, err := a.client.Send(ctx, "DELETE", labelsPath+"/"+url.PathEscape(label.Name), nil)
				switch {
				case err == nil:
					write = reportedLabelList(body)
				case domain.IsNotFound(err) || isPayloadRejection(err):
					if rejected == nil {
						rejected = err
					}
				default:
					return issuekit.LabelWrite{}, err
				}
			}
			return write, rejected
		},
	}
}

func (a *GitHubPRAdapter) readIssueLabels(ctx context.Context, labelsPath string) ([]pullLabel, error) {
	paginator := httpkit.NewLinkPaginator(a.client, labelsPath, url.Values{"per_page": {"100"}}, func(body []byte) ([]pullLabel, error) {
		var labels []pullLabel
		if err := json.Unmarshal(body, &labels); err != nil {
			return nil, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to parse labels response", Err: err}
		}
		return labels, nil
	}, httpkit.PaginatorOptions{
		MaxPages: maxPages,
		OnLimitReached: func(limit int) {
			slog.Warn("pagination limit reached",
				slog.Int("max_pages", limit),
				slog.String("endpoint", labelsPath))
		},
	})
	return paginator.All(ctx)
}

func (a *GitHubPRAdapter) readRepositoryLabelNames(ctx context.Context) ([]string, error) {
	labels, err := a.readRepositoryLabels(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, l.Name)
	}
	return names, nil
}

func (a *GitHubPRAdapter) readRepositoryLabels(ctx context.Context) ([]pullLabel, error) {
	path := "/repos/" + url.PathEscape(a.owner) + "/" + url.PathEscape(a.repo) + "/labels"
	paginator := httpkit.NewLinkPaginator(a.client, path, url.Values{"per_page": {"100"}}, func(body []byte) ([]pullLabel, error) {
		var labels []pullLabel
		if err := json.Unmarshal(body, &labels); err != nil {
			return nil, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to parse labels response", Err: err}
		}
		return labels, nil
	}, httpkit.PaginatorOptions{
		MaxPages: maxPages,
		OnLimitReached: func(limit int) {
			slog.Warn("pagination limit reached",
				slog.Int("max_pages", limit),
				slog.String("endpoint", "/repos/{owner}/{repo}/labels"))
		},
	})
	return paginator.All(ctx)
}

func reportedLabelList(body []byte) issuekit.LabelWrite {
	var labels []pullLabel
	if err := json.Unmarshal(body, &labels); err != nil || labels == nil {
		return issuekit.LabelWrite{}
	}
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = l.Name
	}
	return issuekit.LabelWrite{After: names, Reported: true}
}

func isPayloadRejection(err error) bool {
	var te *domain.TrackerError
	return errors.As(err, &te) && te.Kind == domain.ErrTrackerPayload
}

func (a *GitHubPRAdapter) normalize(pr pullRequest, states issuekit.LabelStates) domain.Issue {
	num := strconv.Itoa(pr.Number)
	desc := ""
	if pr.Body != nil {
		desc = *pr.Body
	}
	assignee := ""
	if len(pr.Assignees) > 0 {
		assignee = pr.Assignees[0].Login
	}
	state := a.model.Derive(states, num, labelNames(pr.Labels), pr.State == "closed")
	issue := domain.Issue{
		ID:                 num,
		Identifier:         num,
		Title:              pr.Title,
		Description:        desc,
		State:              state,
		URL:                pr.HTMLURL,
		Labels:             issuekit.NormalizeLabels(labelNames(pr.Labels)),
		Assignee:           assignee,
		BlockedBy:          []domain.BlockerRef{},
		BlockersUnresolved: false,
		CreatedAt:          pr.CreatedAt,
		UpdatedAt:          pr.UpdatedAt,
	}
	if issue.DisplayID == "" {
		issue.DisplayID = a.owner + "/" + a.repo + "#" + num
	}
	return issue
}
