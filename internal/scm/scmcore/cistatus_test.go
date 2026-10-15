package scmcore_test

import (
	"testing"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/scm/scmcore"
)

func TestAggregateCIStatus(t *testing.T) {
	t.Parallel()

	run := func(status domain.CheckRunStatus, conclusion domain.CheckConclusion) domain.CheckRun {
		return domain.CheckRun{Status: status, Conclusion: conclusion}
	}

	tests := []struct {
		name      string
		runs      []domain.CheckRun
		want      domain.CIStatus
		wantTally scmcore.CheckRunTally
	}{
		{
			name: "nil slice returns Pending",
			runs: nil,
			want: domain.CIStatusPending,
		},
		{
			name: "empty slice returns Pending",
			runs: []domain.CheckRun{},
			want: domain.CIStatusPending,
		},
		{
			name: "all success completed returns Passing",
			runs: []domain.CheckRun{
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionSuccess),
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionSuccess),
			},
			want:      domain.CIStatusPassing,
			wantTally: scmcore.CheckRunTally{Passing: 2},
		},
		{
			name: "one failure returns Failing",
			runs: []domain.CheckRun{
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionFailure),
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionSuccess),
			},
			want:      domain.CIStatusFailing,
			wantTally: scmcore.CheckRunTally{Failing: 1, Passing: 1},
		},
		{
			name: "any in_progress returns Pending",
			runs: []domain.CheckRun{
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionSuccess),
				run(domain.CheckRunStatusInProgress, domain.CheckConclusionPending),
			},
			want:      domain.CIStatusPending,
			wantTally: scmcore.CheckRunTally{Pending: 1, Passing: 1},
		},
		{
			name: "cancelled with success returns Pending",
			runs: []domain.CheckRun{
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionCancelled),
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionSuccess),
			},
			want:      domain.CIStatusPending,
			wantTally: scmcore.CheckRunTally{Pending: 1, Passing: 1},
		},
		{
			name: "timed_out returns Failing",
			runs: []domain.CheckRun{
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionTimedOut),
			},
			want:      domain.CIStatusFailing,
			wantTally: scmcore.CheckRunTally{Failing: 1},
		},
		{
			name: "all cancelled returns Pending",
			runs: []domain.CheckRun{
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionCancelled),
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionCancelled),
			},
			want:      domain.CIStatusPending,
			wantTally: scmcore.CheckRunTally{Pending: 2},
		},
		{
			name: "cancelled with failure returns Failing",
			runs: []domain.CheckRun{
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionCancelled),
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionFailure),
			},
			want:      domain.CIStatusFailing,
			wantTally: scmcore.CheckRunTally{Failing: 1, Pending: 1},
		},
		{
			name: "cancelled with timed_out returns Failing",
			runs: []domain.CheckRun{
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionCancelled),
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionTimedOut),
			},
			want:      domain.CIStatusFailing,
			wantTally: scmcore.CheckRunTally{Failing: 1, Pending: 1},
		},
		{
			name: "cancelled with in_progress returns Pending",
			runs: []domain.CheckRun{
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionCancelled),
				run(domain.CheckRunStatusInProgress, domain.CheckConclusionPending),
			},
			want:      domain.CIStatusPending,
			wantTally: scmcore.CheckRunTally{Pending: 2},
		},
		{
			name: "all neutral and skipped completed returns Passing",
			runs: []domain.CheckRun{
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionNeutral),
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionSkipped),
			},
			want:      domain.CIStatusPassing,
			wantTally: scmcore.CheckRunTally{Passing: 2},
		},
		{
			name: "failure and in_progress returns Failing",
			runs: []domain.CheckRun{
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionFailure),
				run(domain.CheckRunStatusInProgress, domain.CheckConclusionPending),
			},
			want:      domain.CIStatusFailing,
			wantTally: scmcore.CheckRunTally{Failing: 1, Pending: 1},
		},
		{
			name: "all completed with a neutral conclusion yields passing",
			runs: []domain.CheckRun{
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionNeutral),
				run(domain.CheckRunStatusCompleted, domain.CheckConclusionSuccess),
			},
			want:      domain.CIStatusPassing,
			wantTally: scmcore.CheckRunTally{Passing: 2},
		},
		{
			name: "failing conclusion on an unfinished run counts as failing not pending",
			runs: []domain.CheckRun{
				run(domain.CheckRunStatusInProgress, domain.CheckConclusionFailure),
			},
			want:      domain.CIStatusFailing,
			wantTally: scmcore.CheckRunTally{Failing: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := scmcore.AggregateCIStatus(tt.runs)
			if got != tt.want {
				t.Errorf("AggregateCIStatus: got %q, want %q", got, tt.want)
			}

			if gotTally := scmcore.TallyCheckRuns(tt.runs); gotTally != tt.wantTally {
				t.Errorf("TallyCheckRuns(%v) = %+v, want %+v", tt.runs, gotTally, tt.wantTally)
			}
		})
	}
}

func TestFailingCount(t *testing.T) {
	t.Parallel()

	run := func(conclusion domain.CheckConclusion) domain.CheckRun {
		return domain.CheckRun{Status: domain.CheckRunStatusCompleted, Conclusion: conclusion}
	}

	tests := []struct {
		name string
		runs []domain.CheckRun
		want int
	}{
		{"nil returns 0", nil, 0},
		{
			name: "all passing returns 0",
			runs: []domain.CheckRun{
				run(domain.CheckConclusionSuccess),
				run(domain.CheckConclusionNeutral),
				run(domain.CheckConclusionSkipped),
			},
			want: 0,
		},
		{
			name: "one failure",
			runs: []domain.CheckRun{
				run(domain.CheckConclusionFailure),
				run(domain.CheckConclusionSuccess),
			},
			want: 1,
		},
		{
			name: "counts failure and timed_out but not cancelled",
			runs: []domain.CheckRun{
				run(domain.CheckConclusionFailure),
				run(domain.CheckConclusionTimedOut),
				run(domain.CheckConclusionCancelled),
				run(domain.CheckConclusionSuccess),
			},
			want: 2,
		},
		{
			name: "empty slice returns 0",
			runs: []domain.CheckRun{},
			want: 0,
		},
		{
			name: "all passing yields zero",
			runs: []domain.CheckRun{
				run(domain.CheckConclusionSuccess),
				run(domain.CheckConclusionNeutral),
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := scmcore.FailingCount(tt.runs)
			if got != tt.want {
				t.Errorf("FailingCount: got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMergeGate(t *testing.T) {
	t.Parallel()

	run := func(conclusion domain.CheckConclusion) domain.CheckRun {
		return domain.CheckRun{Status: domain.CheckRunStatusCompleted, Conclusion: conclusion}
	}

	tests := []struct {
		name string
		runs []domain.CheckRun
		want scmcore.CIGate
	}{
		{
			name: "empty slice returns CIGateAbsent",
			runs: []domain.CheckRun{},
			want: scmcore.CIGateAbsent,
		},
		{
			name: "cancelled with no failing conclusion returns CIGatePending",
			runs: []domain.CheckRun{
				run(domain.CheckConclusionCancelled),
				run(domain.CheckConclusionSuccess),
			},
			want: scmcore.CIGatePending,
		},
		{
			name: "cancelled with a failing conclusion returns CIGateFailing",
			runs: []domain.CheckRun{
				run(domain.CheckConclusionCancelled),
				run(domain.CheckConclusionFailure),
			},
			want: scmcore.CIGateFailing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := scmcore.MergeGate(tt.runs)
			if got != tt.want {
				t.Errorf("MergeGate: got %q, want %q", got, tt.want)
			}
		})
	}
}
