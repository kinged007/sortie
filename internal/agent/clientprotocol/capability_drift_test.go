package clientprotocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/domain"

	"gopkg.in/yaml.v3"
)

// capabilityDriftProfileEnv is the coordinate the nightly conformance
// job's own environment names the tracked runtime profile through. It
// is owned by this package alongside SORTIE_CLIENTPROTOCOL_TEST and
// SORTIE_CLIENTPROTOCOL_COMMAND, behind the same one gate, so the
// one-gate-per-package rule stays unaffected.
const capabilityDriftProfileEnv = "SORTIE_CLIENTPROTOCOL_PROFILE"

const capabilityDriftCommandEnv = "SORTIE_CLIENTPROTOCOL_COMMAND"

// capabilityGapLabelSet is the four labels a session's capability-gap
// notice may report, in the record's own field order.
var capabilityGapLabelSet = []string{
	capabilityLabelToolServers, capabilityLabelTokenCounts,
	capabilityLabelSessionContinuation, capabilityLabelAgentVersion,
}

// liveCapabilityGapLabels reads the label set out of events' own
// notification stream: every domain.EventNotification message, joined
// in arrival order and separated, searched once per label. The
// separator is what keeps the reading honest: concatenating the
// messages directly lets one message's suffix and the next one's
// prefix spell a label no notification ever reported. A turn whose
// result reports no measured usage also counts as a live token-counts
// gap, since a capability lowered after the once-per-session notice
// leaves no notification of its own for the notice-derived reading to
// find.
func liveCapabilityGapLabels(events []domain.AgentEvent, result domain.TurnResult) []string {
	var joined strings.Builder
	for _, event := range events {
		if event.Type == domain.EventNotification {
			if joined.Len() > 0 {
				joined.WriteByte('\n')
			}
			joined.WriteString(event.Message)
		}
	}
	text := joined.String()

	var labels []string
	for _, label := range capabilityGapLabelSet {
		if strings.Contains(text, label) {
			labels = append(labels, label)
		}
	}
	if !result.UsageMeasured && !slices.Contains(labels, capabilityLabelTokenCounts) {
		labels = append(labels, capabilityLabelTokenCounts)
	}
	sort.Strings(labels)
	return labels
}

func resolveDriftProfilePath(t *testing.T, path string) string {
	t.Helper()
	if filepath.IsAbs(path) {
		return path
	}
	root, err := nightlyRepositoryRoot()
	if err != nil {
		t.Fatalf("resolve repository root for %s=%q: %v", capabilityDriftProfileEnv, path, err)
	}
	return filepath.Join(root, path)
}

type capabilityGapLabelsDocument struct {
	CapabilityGapLabels []string `json:"capability_gap_labels"`
}

// assertCapabilityGapLabelsMatchProfile reads the runtime profile named
// by SORTIE_CLIENTPROTOCOL_PROFILE and compares the live capability-gap
// label set, derived from events' notification stream and result's own
// UsageMeasured verdict, against the profile's own capability_gap_labels,
// failing on a difference in either direction. It skips cleanly when the
// coordinate is absent, so an operator running the conformance suite as
// today is unaffected; a value naming a file it cannot read fails rather
// than skips, so a typo cannot pass green. It carries no Test prefix of
// its own by design: it is called from the event stream and turn result
// an existing conformance turn already collects, spending no turn of its
// own.
func assertCapabilityGapLabelsMatchProfile(t *testing.T, events []domain.AgentEvent, result domain.TurnResult) {
	t.Helper()

	profilePath, present := os.LookupEnv(capabilityDriftProfileEnv)
	if !present || strings.TrimSpace(profilePath) == "" {
		// Returning rather than skipping: this runs inside a caller whose
		// own assertions follow, and skipping would abort them too.
		t.Logf("capability-gap drift comparison not run: %s is unset", capabilityDriftProfileEnv)
		return
	}
	resolvedPath := resolveDriftProfilePath(t, profilePath)
	data, err := os.ReadFile(resolvedPath) //nolint:gosec // the coordinate names a repository-tracked or operator-supplied profile
	if err != nil {
		t.Fatalf("load the profile %s names, %q: %v", capabilityDriftProfileEnv, profilePath, err)
	}
	var doc capabilityGapLabelsDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode the profile %s names, %q: %v", capabilityDriftProfileEnv, profilePath, err)
	}

	live := liveCapabilityGapLabels(events, result)
	want := slices.Clone(doc.CapabilityGapLabels)
	sort.Strings(want)

	if !slices.Equal(live, want) {
		t.Fatalf("live capability-gap labels %v differ from profile %s's capability_gap_labels %v (UsageMeasured=%v, ExitReason=%q)",
			live, profilePath, want, result.UsageMeasured, result.ExitReason)
	}
}

// nightlyWorkflowStep is the subset of one workflow step's shape the
// reachability guard reads.
type nightlyWorkflowStep struct {
	Name string            `yaml:"name"`
	Env  map[string]string `yaml:"env"`
	Run  string            `yaml:"run"`
}

// nightlyWorkflowJob is the subset of one workflow job's shape the
// reachability guard reads.
type nightlyWorkflowJob struct {
	Env      map[string]string `yaml:"env"`
	Strategy struct {
		Matrix struct {
			Include []map[string]string `yaml:"include"`
		} `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []nightlyWorkflowStep `yaml:"steps"`
}

// nightlyWorkflowDocument is the subset of .github/workflows/nightly.yml
// the reachability guard reads.
type nightlyWorkflowDocument struct {
	Jobs map[string]nightlyWorkflowJob `yaml:"jobs"`
}

// nightlyWorkflowRelPath locates the nightly workflow file relative to
// this package directory, the shape a package-relative constant
// already establishes for a package-relative reader in this family.
const nightlyWorkflowRelPath = "../../../.github/workflows/nightly.yml"

// readNightlyWorkflow reads and decodes the nightly workflow document.
func readNightlyWorkflow() (nightlyWorkflowDocument, error) {
	raw, err := os.ReadFile(nightlyWorkflowRelPath) //nolint:gosec // the tracked workflow's own path, resolved from a package-relative constant
	if err != nil {
		return nightlyWorkflowDocument{}, err
	}
	var doc nightlyWorkflowDocument
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nightlyWorkflowDocument{}, fmt.Errorf("decode %s: %w", nightlyWorkflowRelPath, err)
	}
	return doc, nil
}

// nightlyMatrixRowsNamingThisPackage returns every matrix include entry,
// across every job, whose test_path names this package.
func nightlyMatrixRowsNamingThisPackage(doc nightlyWorkflowDocument) []struct {
	jobName string
	job     nightlyWorkflowJob
	row     map[string]string
} {
	var rows []struct {
		jobName string
		job     nightlyWorkflowJob
		row     map[string]string
	}
	for jobName, job := range doc.Jobs {
		for _, row := range job.Strategy.Matrix.Include {
			if strings.Contains(row["test_path"], "internal/agent/clientprotocol") {
				rows = append(rows, struct {
					jobName string
					job     nightlyWorkflowJob
					row     map[string]string
				}{jobName: jobName, job: job, row: row})
			}
		}
	}
	return rows
}

// nightlyGoTestStep returns the one step in steps whose run script
// invokes go test, or false when none does.
func nightlyGoTestStep(steps []nightlyWorkflowStep) (nightlyWorkflowStep, bool) {
	for _, step := range steps {
		if strings.Contains(step.Run, "go test") {
			return step, true
		}
	}
	return nightlyWorkflowStep{}, false
}

// nightlyRepositoryRoot serves both readNightlyWorkflow's caller and
// resolveDriftProfilePath, so no root-module package exists for either alone.
func nightlyRepositoryRoot() (string, error) {
	abs, err := filepath.Abs(filepath.Join(".", nightlyWorkflowRelPath, "..", "..", ".."))
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// callersOfAssertCapabilityGapLabels returns the name of every
// top-level Test function in this package directory whose body calls
// assertCapabilityGapLabelsMatchProfile.
func callersOfAssertCapabilityGapLabels(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	fset := token.NewFileSet()
	var callers []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			calls := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "assertCapabilityGapLabelsMatchProfile" {
					calls = true
				}
				return true
			})
			if calls {
				callers = append(callers, fn.Name.Name)
			}
		}
	}
	return callers
}

// nightlyMatrixValue resolves value against row when it is a single
// "${{ matrix.<key> }}" expression, and returns it unchanged
// otherwise. A workflow that varies a coordinate per shard writes the
// expression rather than a literal, and a guard that could not read
// through it would pass vacuously on exactly the rows it exists to
// check.
func nightlyMatrixValue(row map[string]string, value string) string {
	trimmed := strings.TrimSpace(value)
	match := nightlyMatrixExpression.FindStringSubmatch(trimmed)
	if match == nil {
		return trimmed
	}
	return strings.TrimSpace(row[match[1]])
}

var nightlyMatrixExpression = regexp.MustCompile(`^\$\{\{\s*matrix\.([A-Za-z0-9_-]+)\s*\}\}$`)

func TestLiveCapabilityGapLabelsAddsTokenCountsOnlyWhenUnmeasured(t *testing.T) {
	t.Parallel()

	notify := func(messages ...string) []domain.AgentEvent {
		events := make([]domain.AgentEvent, 0, len(messages))
		for _, message := range messages {
			events = append(events, domain.AgentEvent{Type: domain.EventNotification, Message: message})
		}
		return events
	}

	tests := []struct {
		name   string
		events []domain.AgentEvent
		result domain.TurnResult
		want   []string
	}{
		{
			name:   "an unmeasured result adds token counts to a notice naming a different gap",
			events: notify(capabilityGapNoticeStem + capabilityLabelAgentVersion),
			result: domain.TurnResult{UsageMeasured: false},
			want:   []string{capabilityLabelAgentVersion, capabilityLabelTokenCounts},
		},
		{
			name:   "an unmeasured result whose notice already names token counts adds nothing more",
			events: notify(capabilityGapNoticeStem + capabilityLabelTokenCounts),
			result: domain.TurnResult{UsageMeasured: false},
			want:   []string{capabilityLabelTokenCounts},
		},
		{
			name:   "a measured result leaves notice-derived labels unchanged",
			events: notify(capabilityGapNoticeStem + capabilityLabelToolServers),
			result: domain.TurnResult{UsageMeasured: true},
			want:   []string{capabilityLabelToolServers},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := liveCapabilityGapLabels(tt.events, tt.result)
			if !slices.Equal(got, tt.want) {
				t.Errorf("liveCapabilityGapLabels(...) = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNightlyReachability(t *testing.T) {
	doc, err := readNightlyWorkflow()
	if err != nil {
		t.Fatalf("read the nightly workflow: %v", err)
	}

	rows := nightlyMatrixRowsNamingThisPackage(doc)
	if len(rows) == 0 {
		t.Fatal("nightly workflow names this package's test_path in no matrix row, want at least one")
	}

	callers := callersOfAssertCapabilityGapLabels(t)
	if len(callers) != 1 {
		t.Fatalf("%d top-level Test functions in this package call assertCapabilityGapLabelsMatchProfile, want exactly 1: %v", len(callers), callers)
	}

	root, err := nightlyRepositoryRoot()
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}

	type shard struct {
		command string
		reaches bool
	}
	shards := map[shard]string{}
	commandReached := map[string]bool{}
	for _, row := range rows {
		name := row.row["name"]

		step, found := nightlyGoTestStep(row.job.Steps)
		if !found {
			t.Fatalf("row %q: job %q carries no step whose run script invokes go test", name, row.jobName)
		}

		env := map[string]string{}
		maps.Copy(env, row.job.Env)
		maps.Copy(env, step.Env)

		command := nightlyMatrixValue(row.row, env[capabilityDriftCommandEnv])
		if command == "" {
			t.Errorf("row %q: no %s in the environment job %q's go-test step inherits, or it is empty", name, capabilityDriftCommandEnv, row.jobName)
		}

		runFilter, skipFilter := row.row["run_filter"], row.row["skip_filter"]
		runPattern, runErr := regexp.Compile(runFilter)
		var skipPattern *regexp.Regexp
		var skipErr error
		if skipFilter != "" {
			skipPattern, skipErr = regexp.Compile(skipFilter)
		}
		if compileErr := errors.Join(runErr, skipErr); compileErr != nil {
			t.Errorf("row %q: compile run_filter %q and skip_filter %q as Go regexps: %v", name, runFilter, skipFilter, compileErr)
			continue
		}
		reaches := runPattern.MatchString(callers[0]) && (skipPattern == nil || !skipPattern.MatchString(callers[0]))

		key := shard{command: command, reaches: reaches}
		if previous, duplicate := shards[key]; duplicate {
			t.Errorf("rows %q and %q both drive this package with %s %q and reaches=%v, want one row per runtime and tier", previous, name, capabilityDriftCommandEnv, command, reaches)
		}
		shards[key] = name
		commandReached[command] = commandReached[command] || reaches

		if !reaches {
			continue
		}
		profile := nightlyMatrixValue(row.row, env[capabilityDriftProfileEnv])
		if _, statErr := os.Stat(filepath.Join(root, profile)); profile == "" || statErr != nil {
			t.Errorf("row %q: %s = %q names no existing file under %s", name, capabilityDriftProfileEnv, profile, root)
		}
	}

	for command, reached := range commandReached {
		if !reached {
			t.Errorf("no row that reaches %s drives %s %q", callers[0], capabilityDriftCommandEnv, command)
		}
	}
}
