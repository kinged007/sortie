package persistence

import (
	"context"
	"reflect"
	"testing"
)

func TestUsageLimitHolds(t *testing.T) {
	t.Parallel()

	type holdKey struct {
		agentKind string
		modelOnly bool
		model     string
	}
	kindWide := UsageLimitHold{AgentKind: "claude", ResetsAtMs: 100, HeldSinceMs: 10}
	modelOnlyDefault := UsageLimitHold{AgentKind: "claude", ModelOnly: true, ResetsAtMs: 200, HeldSinceMs: 20}
	modelOnlyNamed := UsageLimitHold{AgentKind: "claude", ModelOnly: true, Model: "opus", ResetsAtMs: 300, HeldSinceMs: 30}
	otherKind := UsageLimitHold{AgentKind: "codex", ModelOnly: true, Model: "gpt", ResetsAtMs: 400, HeldSinceMs: 40}
	replaced := kindWide
	replaced.ResetsAtMs, replaced.HeldSinceMs = 900, 90

	tests := []struct {
		name   string
		save   []UsageLimitHold
		delete []holdKey
		want   []UsageLimitHold
	}{
		{
			name: "load in key order field for field",
			save: []UsageLimitHold{otherKind, modelOnlyNamed, modelOnlyDefault, kindWide},
			want: []UsageLimitHold{kindWide, modelOnlyDefault, modelOnlyNamed, otherKind},
		},
		{
			name: "second save of a key replaces its row",
			save: []UsageLimitHold{kindWide, replaced},
			want: []UsageLimitHold{replaced},
		},
		{
			name:   "delete removes only its key",
			save:   []UsageLimitHold{kindWide, modelOnlyDefault, modelOnlyNamed},
			delete: []holdKey{{"claude", true, ""}},
			want:   []UsageLimitHold{kindWide, modelOnlyNamed},
		},
		{
			name:   "delete of an absent key changes nothing",
			save:   []UsageLimitHold{kindWide},
			delete: []holdKey{{"claude", true, "opus"}, {"codex", false, ""}},
			want:   []UsageLimitHold{kindWide},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			s := openTestStore(t)
			migrateOrFatal(t, s)
			for _, h := range tt.save {
				if err := s.SaveUsageLimitHold(ctx, h); err != nil {
					t.Fatalf("SaveUsageLimitHold(%+v): %v", h, err)
				}
			}
			for _, k := range tt.delete {
				if err := s.DeleteUsageLimitHold(ctx, k.agentKind, k.modelOnly, k.model); err != nil {
					t.Fatalf("DeleteUsageLimitHold(%+v): %v", k, err)
				}
			}

			got, err := s.LoadUsageLimitHolds(ctx)

			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LoadUsageLimitHolds = %+v, %v, want %+v, nil", got, err, tt.want)
			}
		})
	}
}
