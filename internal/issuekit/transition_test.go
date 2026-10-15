package issuekit_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/issuekit"
)

var errWrite = errors.New("write failed")

func transitionStates() issuekit.LabelStates {
	return issuekit.LabelStates{
		Active:   []string{"backlog", "in-progress", "review"},
		Terminal: []string{"done", "wontfix"},
		Handoff:  "awaiting-human",
	}
}

type fakeIssue struct {
	states   issuekit.LabelStates
	labels   []string
	closed   bool
	failAt   int
	calls    int
	readings []string
}

func (f *fakeIssue) reading() string {
	state, _ := f.states.Derive(f.labels, f.closed)
	return state
}

func (f *fakeIssue) write(mutate func()) error {
	f.calls++
	if f.calls == f.failAt {
		return errWrite
	}
	mutate()
	f.readings = append(f.readings, f.reading())
	return nil
}

func (f *fakeIssue) remove(state string) {
	f.labels = slices.DeleteFunc(f.labels, func(name string) bool { return issuekit.SameLabel(name, state) })
}

func (f *fakeIssue) ops(swap bool) issuekit.TransitionOps {
	ops := issuekit.TransitionOps{
		SetClosed: func(_ context.Context, closed bool) error {
			return f.write(func() { f.closed = closed })
		},
	}
	if swap {
		ops.Swap = func(_ context.Context, add string, remove []string) error {
			return f.write(func() {
				if add != "" {
					f.labels = append(f.labels, add)
				}
				for _, state := range remove {
					f.remove(state)
				}
			})
		}
		return ops
	}
	ops.Labels = issuekit.IssueLabelOps{
		Read: func(context.Context) ([]issuekit.IssueLabel, error) {
			read := make([]issuekit.IssueLabel, len(f.labels))
			for i, name := range f.labels {
				read[i] = issuekit.IssueLabel{Name: name}
			}
			return read, nil
		},
		Add: func(_ context.Context, label string) (issuekit.LabelWrite, error) {
			err := f.write(func() { f.labels = append(f.labels, label) })
			return issuekit.LabelWrite{After: slices.Clone(f.labels), Reported: true}, err
		},
		Remove: func(_ context.Context, labels []issuekit.IssueLabel) (issuekit.LabelWrite, error) {
			err := f.write(func() {
				for _, l := range labels {
					f.remove(l.Name)
				}
			})
			return issuekit.LabelWrite{After: slices.Clone(f.labels), Reported: true}, err
		},
	}
	return ops
}

func (f *fakeIssue) transition(t *testing.T, swap bool, target string) error {
	t.Helper()
	plan, err := f.states.PlanTransition(target, f.labels, f.closed)
	if err != nil {
		return err
	}
	return issuekit.ApplyTransition(t.Context(), plan, f.ops(swap))
}

func TestPlanAndApplyTransition(t *testing.T) {
	t.Parallel()

	starts := []struct {
		name   string
		labels []string
		closed bool
	}{
		{"open with no state label", []string{"bug"}, false},
		{"open with one state label", []string{"Backlog", "bug"}, false},
		{"open with several state labels", []string{"backlog", "review", "awaiting-human", "bug"}, false},
		{"open with terminal and active labels", []string{"done", "backlog"}, false},
		{"closed with no state label", []string{"bug"}, true},
		{"closed with a terminal label", []string{"wontfix"}, true},
		{"closed with an active label", []string{"backlog"}, true},
		{"closed with terminal and active labels", []string{"done", "backlog"}, true},
	}
	targets := []struct {
		name         string
		terminal     bool
		invalid      bool
		refusedClose bool
	}{
		{name: "review"},
		{name: "awaiting-human", refusedClose: true},
		{name: "wontfix", terminal: true},
		{name: "bogus", invalid: true},
	}

	for _, start := range starts {
		for _, target := range targets {
			for _, swap := range []bool{false, true} {
				name := start.name + " to " + target.name
				if swap {
					name += " via swap"
				}
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					fresh := func(failAt int) *fakeIssue {
						return &fakeIssue{states: transitionStates(), labels: slices.Clone(start.labels), closed: start.closed, failAt: failAt}
					}

					f := fresh(0)
					startState := f.reading()
					err := f.transition(t, swap, target.name)

					if target.invalid || (start.closed && target.refusedClose) {
						var te *domain.TrackerError
						if !errors.As(err, &te) || te.Kind != domain.ErrTrackerPayload {
							t.Fatalf("PlanTransition(%q, %v, closed=%v) error = %v, want ErrTrackerPayload", target.name, start.labels, start.closed, err)
						}
						if f.calls != 0 {
							t.Errorf("refused transition sent %d writes, want 0", f.calls)
						}
						return
					}
					if err != nil {
						t.Fatalf("transition(%q) = %v, want nil", target.name, err)
					}

					writes := f.calls
					if _, carried := f.states.Derive(f.labels, f.closed); !slices.Equal(carried, []string{target.name}) {
						t.Errorf("labels after transition(%q) = %v, carried states = %v, want exactly %q", target.name, f.labels, carried, target.name)
					}
					if f.closed != target.terminal {
						t.Errorf("closed after transition(%q) = %v, want %v", target.name, f.closed, target.terminal)
					}
					assertReadingsWithin(t, f, startState, target.name)

					for failAt := 1; failAt <= writes; failAt++ {
						f := fresh(failAt)
						err := f.transition(t, swap, target.name)

						if !errors.Is(err, errWrite) {
							t.Fatalf("transition(%q) failing write %d = %v, want errWrite", target.name, failAt, err)
						}
						assertReadingsWithin(t, f, startState, target.name)
					}
				})
			}
		}
	}
}

func assertReadingsWithin(t *testing.T, f *fakeIssue, start, target string) {
	t.Helper()
	for _, reading := range append(slices.Clone(f.readings), f.reading()) {
		if reading != start && reading != target {
			t.Errorf("reading %q between writes, want start %q or target %q", reading, start, target)
		}
	}
}
