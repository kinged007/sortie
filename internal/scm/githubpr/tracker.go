// Package githubpr registers the "github-pr" tracker kind: GitHub pull
// requests surfaced as Sortie issues. Reads go to the Pulls REST API
// (which returns PRs the issues-route adapter skips); label writes go
// to the Issues routes (which accept PR numbers as issue numbers).
package githubpr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/httpkit"
	"github.com/sortie-ai/sortie/internal/issuekit"
	"github.com/sortie-ai/sortie/internal/registry"
	"github.com/sortie-ai/sortie/internal/trackermetrics"
	"github.com/sortie-ai/sortie/internal/typeutil"
)

func init() {
	registry.Trackers.RegisterWithMeta("github-pr", NewGitHubPRAdapter, registry.TrackerMeta{
		RequiresProject:       true,
		RequiresAPIKey:        true,
		ValidateTrackerConfig: validateConfig,
		DefaultActiveStates:   defaultActiveStates,
		DefaultTerminalStates: defaultTerminalStates,
		BlockerSource:         registry.BlockersPerIssue,
	})
}

var _ domain.TrackerAdapter = (*GitHubPRAdapter)(nil)
var _ domain.BlockerReader = (*GitHubPRAdapter)(nil)

const maxPages = 200

var defaultActiveStates = issuekit.DefaultActiveLabelStates()
var defaultTerminalStates = issuekit.DefaultTerminalLabelStates()

// GitHubPRAdapter implements [domain.TrackerAdapter] against the GitHub
// Pulls REST API for reads and the Issues REST API for label writes.
// Safe for concurrent use.
type GitHubPRAdapter struct {
	client         *httpkit.Client
	owner          string
	repo           string
	activeStates   []string
	terminalStates []string
	handoffState   string
	queryFilter    string
	metrics        domain.Metrics
	log            *slog.Logger
}

// NewGitHubPRAdapter creates a [GitHubPRAdapter] from adapter
// configuration. Required config keys: "api_key", "project"
// (owner/repo format). Optional: "endpoint", "active_states",
// "terminal_states", "handoff_state", "query_filter", "user_agent".
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

	handoffRaw, fault := typeutil.StringField(config, "handoff_state")
	if fault != nil {
		return nil, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: fault.Error()}
	}
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
		client:         newClient(endpoint, apiKey, userAgent),
		owner:          owner,
		repo:           repo,
		activeStates:   lowerStates(typeutil.ExtractStringSlice(config["active_states"]), defaultActiveStates),
		terminalStates: lowerStates(typeutil.ExtractStringSlice(config["terminal_states"]), defaultTerminalStates),
		handoffState:   lowerSingle(handoffRaw),
		queryFilter:    queryFilter,
		log:            slog.Default(),
	}, nil
}

func splitProject(project string) (owner, repo string, ok bool) {
	o, r, cut := splitCut(project, "/")
	if !cut || o == "" || r == "" || containsSlash(r) {
		return "", "", false
	}
	return o, r, true
}

// FetchCandidateIssues returns open pull requests carrying a configured
// active-state label. Comments are nil on all returned issues. When
// query_filter is set, candidates are additionally matched against it:
// a "label:<name>" clause keeps only PRs carrying that label (comma
// separates OR alternatives), an "assignee:<login>" clause keeps only
// PRs assigned to that login (with "@me" resolved to the API token
// owner via /user), and a "-label:<name>" clause drops any PR carrying
// that label. All three matter here because this path lists via /pulls,
// which cannot run search syntax — without client-side matching they
// would be silently ignored: escalated PRs would be re-dispatched, and
// (via DeriveLabelState's fallback to activeStates[0]) any unlabeled
// open PR would derive the first active state and be dispatched.
func (a *GitHubPRAdapter) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	issues := make([]domain.Issue, 0)
	err := trackermetrics.Track(a.metrics, "fetch_candidates", func() error {
		filter, fetchErr := a.resolvedQueryFilter(ctx)
		if fetchErr != nil {
			return fetchErr
		}
		fetched, fetchErr := a.fetchOpenPRs(ctx)
		if fetchErr != nil {
			return fetchErr
		}
		activeSet := toSet(a.activeStates)
		for _, issue := range fetched {
			if _, ok := activeSet[issue.State]; !ok {
				continue
			}
			if !filter.match(issue) {
				continue
			}
			issue.Comments = nil
			issues = append(issues, issue)
		}
		return nil
	})
	return issues, err
}

// queryFilter carries the parsed "label:<name>" requirements (each
// clause an OR-set; every clause needs one hit), the parsed
// "assignee:<login>" clause (if any), plus every "-label:<name>"
// exclusion.
type queryFilter struct {
	assignee queryAssignee
	required [][]string
	excluded []string
}

// queryAssignee carries one parsed "assignee:<login>" clause, or none.
type queryAssignee struct {
	login string
	found bool
}

// resolvedQueryFilter parses the adapter's query_filter into matchable
// clauses. An empty filter matches everything. Label, assignee, and
// negative-label clauses all apply to the pulls-list path because it
// cannot run search syntax. "@me" resolves to the token owner's login
// via /user.
func (a *GitHubPRAdapter) resolvedQueryFilter(ctx context.Context) (queryFilter, error) {
	filter := queryFilter{
		required: parseRequiredLabels(a.queryFilter),
		excluded: parseExcludedLabels(a.queryFilter),
	}
	assignee, found := parseAssigneeClause(a.queryFilter)
	if !found {
		return filter, nil
	}
	if !strings.EqualFold(assignee, "@me") {
		filter.assignee = queryAssignee{login: assignee, found: true}
		return filter, nil
	}
	login, err := a.currentLogin(ctx)
	if err != nil {
		return queryFilter{}, err
	}
	filter.assignee = queryAssignee{login: login, found: true}
	return filter, nil
}

// match reports whether issue satisfies the parsed filter. A filter
// without an assignee clause matches every issue on assignee;
// exclusion labels reject regardless; every label: clause needs one
// of its alternatives present. Matching is case-insensitive;
// unassigned PRs never match an assignee clause.
// ponytail: domain.Issue carries the first assignee only; a PR where
// the user is a second assignee won't match — extend the domain type
// with all assignees when multi-assignee routing is needed.
func (f queryFilter) match(issue domain.Issue) bool {
	if f.assignee.found {
		if issue.Assignee == "" {
			return false
		}
		if !strings.EqualFold(issue.Assignee, f.assignee.login) {
			return false
		}
	}
	if len(f.excluded) > 0 || len(f.required) > 0 {
		present := toSetLower(issue.Labels)
		for _, label := range f.excluded {
			if _, ok := present[label]; ok {
				return false
			}
		}
		for _, alternatives := range f.required {
			hit := false
			for _, label := range alternatives {
				if _, ok := present[label]; ok {
					hit = true
					break
				}
			}
			if !hit {
				return false
			}
		}
	}
	return true
}

// parseRequiredLabels extracts every "label:<name>" clause from a
// search-style filter string as an OR-set per clause (comma separates
// alternatives, as in GitHub search). Values are lowercased for
// comparison against the adapter-normalized issue labels.
func parseRequiredLabels(filter string) [][]string {
	var required [][]string
	for _, field := range strings.Fields(filter) {
		name, value, cut := splitCut(field, ":")
		if !cut || !strings.EqualFold(name, "label") {
			continue
		}
		var alternatives []string
		for _, alt := range strings.Split(strings.Trim(value, `"`), ",") {
			alt = strings.ToLower(strings.TrimSpace(alt))
			if alt == "" {
				continue
			}
			alternatives = append(alternatives, alt)
		}
		if len(alternatives) > 0 {
			required = append(required, alternatives)
		}
	}
	return required
}

// parseExcludedLabels extracts every "-label:<name>" clause from a
// search-style filter string, lowercased for comparison against the
// adapter-normalized issue labels.
func parseExcludedLabels(filter string) []string {
	var excluded []string
	for _, field := range strings.Fields(filter) {
		name, value, cut := splitCut(field, ":")
		if !cut || !strings.EqualFold(name, "-label") {
			continue
		}
		value = strings.ToLower(strings.Trim(value, `"`))
		if value == "" {
			continue
		}
		excluded = append(excluded, value)
	}
	return excluded
}

// parseAssigneeClause extracts the login from the first
// "assignee:<login>" clause in a search-style filter string.
func parseAssigneeClause(filter string) (string, bool) {
	for _, field := range strings.Fields(filter) {
		name, value, cut := splitCut(field, ":")
		if !cut || !strings.EqualFold(name, "assignee") {
			continue
		}
		value = strings.Trim(value, `"`)
		if value == "" {
			continue
		}
		return value, true
	}
	return "", false
}

// currentLogin returns the login of the API token owner via GET /user.
func (a *GitHubPRAdapter) currentLogin(ctx context.Context) (string, error) {
	body, _, err := a.client.Get(ctx, "/user", nil)
	if err != nil {
		return "", err
	}
	var user pullUser
	if err := json.Unmarshal(body, &user); err != nil {
		return "", &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to parse user response", Err: err}
	}
	if user.Login == "" {
		return "", &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "empty login in user response"}
	}
	return user.Login, nil
}

func (a *GitHubPRAdapter) fetchOpenPRs(ctx context.Context) ([]domain.Issue, error) {
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
			issue := a.normalize(pr)
			issues = append(issues, issue)
		}
		return issues, nil
	}, httpkit.PaginatorOptions{MaxPages: maxPages})

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

		issue = a.normalize(pr)
		issue.BlockedBy = []domain.BlockerRef{}
		issue.BlockersUnresolved = false

		issue.Comments, err = a.FetchIssueComments(ctx, issueID)
		return err
	})
	return issue, err
}

// FetchIssuesByStates returns pull requests in the specified states:
// open PRs via the pulls list endpoint for non-terminal states,
// closed PRs via search for terminal states.
func (a *GitHubPRAdapter) FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error) {
	if len(states) == 0 {
		return []domain.Issue{}, nil
	}
	stateSet := toSetLower(states)

	var requestedTerminal []string
	needOpenFetch := false
	for s := range stateSet {
		if isTerminal(s, a.terminalStates) {
			requestedTerminal = append(requestedTerminal, s)
		} else {
			needOpenFetch = true
		}
	}

	filter, fetchErr := a.resolvedQueryFilter(ctx)
	if fetchErr != nil {
		return []domain.Issue{}, fetchErr
	}

	var matched []domain.Issue
	err := trackermetrics.Track(a.metrics, "fetch_by_states", func() error {
		matchedIssues := make([]domain.Issue, 0)
		seen := make(map[string]struct{})

		if needOpenFetch {
			prs, err := a.fetchOpenPRs(ctx)
			if err != nil {
				return err
			}
			for _, issue := range prs {
				if _, ok := stateSet[issue.State]; !ok {
					continue
				}
				if !filter.match(issue) {
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
			issues, err := a.fetchClosedPRsByLabel(ctx, label, seen)
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

func (a *GitHubPRAdapter) fetchClosedPRsByLabel(ctx context.Context, label string, seen map[string]struct{}) ([]domain.Issue, error) {
	q := fmt.Sprintf(`repo:%s/%s type:pr state:closed label:"%s"`, a.owner, a.repo, label)
	params := url.Values{
		"q":        {q},
		"sort":     {"created"},
		"order":    {"asc"},
		"per_page": {"50"},
	}
	if a.queryFilter != "" {
		params.Set("q", q+" "+a.queryFilter)
	}

	paginator := httpkit.NewLinkPaginator(a.client, "/search/issues", params, func(body []byte) ([]domain.Issue, error) {
		var page searchResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to parse search response", Err: err}
		}
		issues := make([]domain.Issue, 0, len(page.Items))
		for _, item := range page.Items {
			pr := item.asPullRequest()
			issue := a.normalize(pr)
			if _, dup := seen[issue.Identifier]; dup {
				continue
			}
			issue.Comments = nil
			issues = append(issues, issue)
			seen[issue.Identifier] = struct{}{}
		}
		return issues, nil
	}, httpkit.PaginatorOptions{MaxPages: maxPages})

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
		states[num] = a.stateOf(pr)
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
		}, httpkit.PaginatorOptions{MaxPages: maxPages})

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

// FetchIssueBlockers returns an empty list: PRs carry no blocker graph
// in this adapter. The non-nil slice satisfies the per-issue contract
// without a dependencies route.
func (a *GitHubPRAdapter) FetchIssueBlockers(ctx context.Context, issueID string) ([]domain.BlockerRef, error) {
	blockers := make([]domain.BlockerRef, 0)
	err := trackermetrics.Track(a.metrics, "fetch_blockers", func() error {
		return nil
	})
	return blockers, err
}

// TransitionIssue moves a PR to the target state by swapping the state
// label via the issues labels route and closing or reopening the PR.
func (a *GitHubPRAdapter) TransitionIssue(ctx context.Context, issueID string, targetState string) error {
	targetLower := lowerSingle(targetState)

	return trackermetrics.Track(a.metrics, "transition", func() error {
		isHandoffTarget := a.handoffState != "" && targetLower == a.handoffState
		if !isActive(targetLower, a.activeStates) && !isTerminal(targetLower, a.terminalStates) && !isHandoffTarget {
			return &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: fmt.Sprintf("invalid target state: %q is not a configured active, terminal, or handoff state", targetState)}
		}

		prPath := "/repos/" + a.owner + "/" + a.repo + "/pulls/" + url.PathEscape(issueID)
		body, _, err := a.client.Get(ctx, prPath, nil)
		if err != nil {
			return err
		}
		var pr pullRequest
		if err := json.Unmarshal(body, &pr); err != nil {
			return &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to parse pull response", Err: err}
		}

		labels := labelNames(pr.Labels)
		currentLabel := issuekit.CurrentLabelState(labels,
			issuekit.LabelStates{Active: a.activeStates, Terminal: a.terminalStates, Handoff: a.handoffState})
		currentNative := "open"
		if pr.State == "closed" {
			currentNative = "closed"
		}

		issuePath := "/repos/" + a.owner + "/" + a.repo + "/issues/" + url.PathEscape(issueID)
		if currentLabel != "" && currentLabel != targetLower {
			labelPath := issuePath + "/labels/" + url.PathEscape(currentLabel)
			if err := a.client.SendNoBody(ctx, "DELETE", labelPath); err != nil && !domain.IsNotFound(err) {
				return err
			}
		}
		if currentLabel != targetLower {
			payload, err := json.Marshal(map[string][]string{"labels": {targetLower}})
			if err != nil {
				return &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to marshal label payload", Err: err}
			}
			if _, err := a.client.Send(ctx, "POST", issuePath+"/labels", bytes.NewReader(payload)); err != nil {
				return err
			}
		}

		if isTerminal(targetLower, a.terminalStates) && currentNative == "open" {
			payload, err := json.Marshal(map[string]any{"state": "closed"})
			if err != nil {
				return &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to marshal state payload", Err: err}
			}
			if _, err := a.client.Send(ctx, "PATCH", prPath, bytes.NewReader(payload)); err != nil {
				return err
			}
		} else if isActive(targetLower, a.activeStates) && currentNative == "closed" {
			payload, err := json.Marshal(map[string]any{"state": "open"})
			if err != nil {
				return &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to marshal state payload", Err: err}
			}
			if _, err := a.client.Send(ctx, "PATCH", prPath, bytes.NewReader(payload)); err != nil {
				return err
			}
		}
		return nil
	})
}

// CommentIssue posts a Markdown comment on the specified PR via the
// issues comments route.
func (a *GitHubPRAdapter) CommentIssue(ctx context.Context, issueID string, text string) error {
	return trackermetrics.Track(a.metrics, "comment", func() error {
		path := "/repos/" + a.owner + "/" + a.repo + "/issues/" + url.PathEscape(issueID) + "/comments"
		payload, err := json.Marshal(map[string]string{"body": text})
		if err != nil {
			return &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to marshal comment payload", Err: err}
		}
		_, err = a.client.Send(ctx, "POST", path, bytes.NewReader(payload))
		return err
	})
}

// AddLabel adds a label to the specified PR via the issues labels route.
func (a *GitHubPRAdapter) AddLabel(ctx context.Context, issueID string, label string) error {
	return trackermetrics.Track(a.metrics, "add_label", func() error {
		path := "/repos/" + a.owner + "/" + a.repo + "/issues/" + url.PathEscape(issueID) + "/labels"
		payload, err := json.Marshal(map[string][]string{"labels": {label}})
		if err != nil {
			return &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: "failed to marshal label payload", Err: err}
		}
		_, err = a.client.Send(ctx, "POST", path, bytes.NewReader(payload))
		return err
	})
}

// SetMetrics configures the metrics recorder for tracker API call
// instrumentation. When not called or called with nil, the adapter
// operates without recording metrics.
func (a *GitHubPRAdapter) SetMetrics(m domain.Metrics) {
	a.metrics = m
}

func (a *GitHubPRAdapter) stateOf(pr pullRequest) string {
	states := issuekit.LabelStates{Active: a.activeStates, Terminal: a.terminalStates, Handoff: a.handoffState}
	return issuekit.DeriveLabelState(labelNames(pr.Labels), pr.State, "open", "closed", states, strconv.Itoa(pr.Number), a.log)
}

func (a *GitHubPRAdapter) normalize(pr pullRequest) domain.Issue {
	num := strconv.Itoa(pr.Number)
	desc := ""
	if pr.Body != nil {
		desc = *pr.Body
	}
	assignee := ""
	if len(pr.Assignees) > 0 {
		assignee = pr.Assignees[0].Login
	}
	issue := domain.Issue{
		ID:                 num,
		Identifier:         num,
		Title:              pr.Title,
		Description:        desc,
		State:              a.stateOf(pr),
		URL:                pr.HTMLURL,
		Labels:             issuekit.NormalizeLabels(labelNames(pr.Labels)),
		Assignee:           assignee,
		BlockedBy:          []domain.BlockerRef{},
		BlockersUnresolved: true,
		CreatedAt:          pr.CreatedAt,
		UpdatedAt:          pr.UpdatedAt,
	}
	if issue.DisplayID == "" {
		issue.DisplayID = a.owner + "/" + a.repo + "#" + num
	}
	return issue
}
