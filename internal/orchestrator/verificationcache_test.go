package orchestrator

import (
	"cmp"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sortie-ai/sortie/internal/domain"
)

func TestVerificationKeyFor(t *testing.T) {
	t.Parallel()

	base := func() domain.StartSessionParams {
		return domain.StartSessionParams{
			WorkspacePath: "/ws/one", ResumeSessionID: "resume-1", MCPConfigPath: "/ws/one/mcp.json", SSHHost: "host-a",
			AgentConfig: domain.AgentConfig{CommandArgv: []string{"agent", "--serve"}},
			SSHEnvNames: []string{"B", "A"},
			Settings:    map[string]any{"model": "m1"},
		}
	}
	tests := []struct {
		name    string
		kind    string
		mutate  func(p *domain.StartSessionParams)
		differs bool
	}{
		{"kind", "other", func(*domain.StartSessionParams) {}, true},
		{"command form", "", func(p *domain.StartSessionParams) { p.AgentConfig = domain.AgentConfig{Command: "agent --serve"} }, true},
		{"argv element", "", func(p *domain.StartSessionParams) { p.AgentConfig.CommandArgv[1] = "--other" }, true},
		{"argv element boundary", "", func(p *domain.StartSessionParams) { p.AgentConfig.CommandArgv = []string{"agent-", "-serve"} }, true},
		{"ssh host", "", func(p *domain.StartSessionParams) { p.SSHHost = "host-b" }, true},
		{"env names", "", func(p *domain.StartSessionParams) { p.SSHEnvNames = []string{"A"} }, true},
		{"settings value", "", func(p *domain.StartSessionParams) { p.Settings["model"] = "m2" }, true},
		{"env names order", "", func(p *domain.StartSessionParams) { p.SSHEnvNames = []string{"A", "B"} }, false},
		{"resume fields", "", func(p *domain.StartSessionParams) {
			p.ResumeSessionID, p.MCPConfigPath = "resume-2", "/ws/two/mcp.json"
		}, false},
	}
	baseKey, _ := verificationKeyFor("agent", base())

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			params := base()
			tt.mutate(&params)

			key, keyed := verificationKeyFor(cmp.Or(tt.kind, "agent"), params)

			if !keyed || (key != baseKey) != tt.differs {
				t.Errorf("verificationKeyFor(%s) = (differs from base: %v, keyed: %v), want (%v, true)", tt.name, key != baseKey, keyed, tt.differs)
			}
		})
	}

	if _, keyed := verificationKeyFor("agent", domain.StartSessionParams{Settings: map[string]any{"x": make(chan int)}}); keyed {
		t.Error("verificationKeyFor(unencodable settings) keyed = true, want false")
	}
}

func TestVerificationCacheWindow(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cache, key := newVerificationCache(), verificationKey{1}
		cache.record(key, time.Now())

		time.Sleep(verificationWindow - time.Nanosecond)
		_, inside := cache.fresh(key, time.Now())
		time.Sleep(time.Nanosecond)
		_, atEdge := cache.fresh(key, time.Now())
		var nilCache *verificationCache
		nilCache.record(key, time.Now())
		nilCache.evict(key)
		_, nilFresh := nilCache.fresh(key, time.Now())

		if !inside || atEdge || nilFresh {
			t.Errorf("fresh(proof+window-1ns, proof+window, nil cache) = %v, %v, %v, want true, false, false", inside, atEdge, nilFresh)
		}
	})
}
