package server

import (
	"maps"
	"testing"
)

func TestPRLoopOf(t *testing.T) {
	t.Parallel()

	keys := []string{"ci_failure", "review_comments", "label_commands"}
	unrecordedAll := map[string]prLoopStationResponse{
		"ci_failure":      {Result: "unrecorded"},
		"review_comments": {Result: "unrecorded"},
		"label_commands":  {Result: "unrecorded"},
	}
	with := func(overrides map[string]prLoopStationResponse) map[string]prLoopStationResponse {
		out := maps.Clone(unrecordedAll)
		maps.Copy(out, overrides)
		return out
	}

	tests := []struct {
		name     string
		activity PassActivity
		want     map[string]prLoopStationResponse
	}{
		{
			name:     "pass not recorded reads unrecorded at every key",
			activity: PassActivity{Kinds: map[string]KindActivity{"ci": {Watched: true, Acted: true}}},
			want:     unrecordedAll,
		},
		{
			name:     "recorded pass with no kind of a key reads unrecorded there",
			activity: PassActivity{Recorded: true, Kinds: map[string]KindActivity{"review": {Watched: true}}},
			want:     with(map[string]prLoopStationResponse{"review_comments": {Result: "quiet"}}),
		},
		{
			name:     "escalated outranks acted",
			activity: PassActivity{Recorded: true, Kinds: map[string]KindActivity{"ci": {Watched: true, Acted: true, Escalated: true, Continuations: 2}}},
			want:     with(map[string]prLoopStationResponse{"ci_failure": {Result: "escalated", Continuations: 2}}),
		},
		{
			name:     "acted by action alone",
			activity: PassActivity{Recorded: true, Kinds: map[string]KindActivity{"ci": {Acted: true}}},
			want:     with(map[string]prLoopStationResponse{"ci_failure": {Result: "acted"}}),
		},
		{
			name:     "acted by continuations alone",
			activity: PassActivity{Recorded: true, Kinds: map[string]KindActivity{"ci": {Continuations: 3}}},
			want:     with(map[string]prLoopStationResponse{"ci_failure": {Result: "acted", Continuations: 3}}),
		},
		{
			name:     "quiet on a watch alone",
			activity: PassActivity{Recorded: true, Kinds: map[string]KindActivity{"ci": {Watched: true}}},
			want:     with(map[string]prLoopStationResponse{"ci_failure": {Result: "quiet"}}),
		},
		{
			name:     "watch plus one continuation is acted once",
			activity: PassActivity{Recorded: true, Kinds: map[string]KindActivity{"ci": {Watched: true, Continuations: 1}}},
			want:     with(map[string]prLoopStationResponse{"ci_failure": {Result: "acted", Continuations: 1}}),
		},
		{
			name: "label kinds fold into label_commands with continuations summed",
			activity: PassActivity{Recorded: true, Kinds: map[string]KindActivity{
				"label-review": {Watched: true, Continuations: 1},
				"label-fix":    {Watched: true, Acted: true, Continuations: 2},
			}},
			want: with(map[string]prLoopStationResponse{"label_commands": {Result: "acted", Continuations: 3}}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := prLoopOf(tt.activity, keys)

			if !maps.Equal(got, tt.want) {
				t.Errorf("prLoopOf(%+v) = %v, want %v", tt.activity, got, tt.want)
			}
		})
	}
}
