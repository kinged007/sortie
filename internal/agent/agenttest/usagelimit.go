package agenttest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/registry"
)

// UsageLimitRefusalCase is one turn replaying a usage-limit refusal and
// the normalized outcome it must end with.
type UsageLimitRefusalCase struct {
	Name          string
	Adapter       domain.AgentAdapter
	Start         domain.StartSessionParams
	Turn          domain.RunTurnParams
	WantResetsAt  time.Time
	WantModelOnly bool
}

// AssertUsageLimitRefusal fails t unless kind is registered with
// [registry.UsageLimitRecognized], cases is non-empty, and every case's
// turn ends with a [domain.ErrUsageLimit] error carrying the case's
// reset instant and model scope.
func AssertUsageLimitRefusal(t *testing.T, kind string, cases []UsageLimitRefusalCase) {
	t.Helper()

	meta, registered := registry.Agents.Meta(kind)
	if !registered {
		t.Fatalf("kind %q is not registered", kind)
	}
	if meta.UsageLimitRecognition != registry.UsageLimitRecognized {
		t.Fatalf("kind %q declares usage-limit recognition %q, want %q", kind, meta.UsageLimitRecognition, registry.UsageLimitRecognized)
	}
	if len(cases) == 0 {
		t.Fatalf("kind %q: no usage-limit refusal cases", kind)
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) { assertUsageLimitRefusalCase(t, tc) })
	}
}

func assertUsageLimitRefusalCase(t *testing.T, tc UsageLimitRefusalCase) {
	t.Helper()

	ctx := context.Background()
	session, err := tc.Adapter.StartSession(ctx, tc.Start)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() {
		if stopErr := tc.Adapter.StopSession(context.WithoutCancel(ctx), session); stopErr != nil {
			t.Logf("StopSession: %v", stopErr)
		}
	}()

	turn := tc.Turn
	if turn.OnEvent == nil {
		turn.OnEvent = func(domain.AgentEvent) {}
	}
	_, runErr := tc.Adapter.RunTurn(ctx, session, turn)

	var agentErr *domain.AgentError
	if !errors.As(runErr, &agentErr) {
		t.Fatalf("RunTurn error = %v, want *domain.AgentError", runErr)
	}
	if agentErr.Kind != domain.ErrUsageLimit {
		t.Fatalf("error kind = %q, want %q", agentErr.Kind, domain.ErrUsageLimit)
	}
	if agentErr.UsageLimit == nil {
		t.Fatalf("error kind %q carries no UsageLimit", agentErr.Kind)
	}
	if !agentErr.UsageLimit.ResetsAt.Equal(tc.WantResetsAt) {
		t.Errorf("ResetsAt = %v, want %v", agentErr.UsageLimit.ResetsAt, tc.WantResetsAt)
	}
	if agentErr.UsageLimit.ModelOnly != tc.WantModelOnly {
		t.Errorf("ModelOnly = %v, want %v", agentErr.UsageLimit.ModelOnly, tc.WantModelOnly)
	}
}
