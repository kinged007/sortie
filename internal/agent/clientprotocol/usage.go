package clientprotocol

import (
	"context"
	"encoding/json"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/agent/clientprotocol/usagesource"
	"github.com/sortie-ai/sortie/internal/registry"
)

// usageReader supplies token measurements read outside the wire, for a
// runtime whose protocol carries no spend counter. A reader must claim the
// resolved launch target, recognize the runtime name the handshake reports,
// and produce a record on its first drain; failing any of those drops the
// reader and leaves the session unmeasured with the token ceiling inactive.
//
// Drain reports whether a measurement exists at all; Completeness grades how
// far the figure it just returned was proven to reach.
type usageReader interface {
	Claim(target agentcore.LaunchTarget, runtime string) ([]string, bool)
	Recognize(name string) bool
	Open(sessionID string)
	Drain(ctx context.Context, lowerBound int64) (agentcore.RecoveredUsage, string, bool)
	Completeness() usagesource.Completeness
	Close()
}

// usageClaim is one source's accepted claim: the source itself and the
// environment assignments its launch needs.
type usageClaim struct {
	source      usageReader
	assignments []string
}

// usageClaims is one offer round's outcome, in registry order: every source
// that claimed the launch, and every source that refused it.
type usageClaims struct {
	claimed   []usageClaim
	unclaimed []usageReader
}

// usageVerdict is one start's handshake settlement of a usageClaims:
// confirmed is the claimed source the handshake recognized, nil when none
// was; released is every other claimed source; recognizer is, only after the
// first start found no confirmed source, the unclaimed source that
// recognizes the runtime's name.
type usageVerdict struct {
	confirmed  *usageClaim
	released   []usageReader
	recognizer usageReader
}

// claimUsageSources offers target to a fresh instance of every source
// registered in sources, in registry order, and buckets each by whether its
// first offer, with an empty runtime, accepts.
func claimUsageSources(sources *registry.Registry[usagesource.Constructor, struct{}], target agentcore.LaunchTarget) usageClaims {
	var claims usageClaims
	for _, kind := range sources.Kinds() {
		constructor, _ := sources.Get(kind) // a listed kind always resolves
		source := constructor()
		if assignments, ok := source.Claim(target, ""); ok {
			claims.claimed = append(claims.claimed, usageClaim{source: source, assignments: assignments})
			continue
		}
		claims.unclaimed = append(claims.unclaimed, source)
	}
	return claims
}

// settleUsageSources settles claims against one start's handshake. info is
// the handshake's reported agent implementation, nil when the handshake
// carried none; remote is whether the launch was remote. The first claimed
// source, in claim order, whose Recognize accepts the reported name becomes
// confirmed; every other claimed source is released. Only when no claimed
// source is confirmed, the reported name is non-empty, and the launch is not
// remote, the first unclaimed source whose Recognize accepts that name
// becomes the recognizer.
func settleUsageSources(claims usageClaims, info *implementation, remote bool) usageVerdict {
	name := ""
	if info != nil {
		name = info.Name
	}

	confirmedIndex := -1
	if name != "" {
		for i := range claims.claimed {
			if claims.claimed[i].source.Recognize(name) {
				confirmedIndex = i
				break
			}
		}
	}

	var verdict usageVerdict
	for i := range claims.claimed {
		if i == confirmedIndex {
			verdict.confirmed = &claims.claimed[i]
			continue
		}
		verdict.released = append(verdict.released, claims.claimed[i].source)
	}
	if verdict.confirmed == nil && name != "" && !remote {
		for _, source := range claims.unclaimed {
			if source.Recognize(name) {
				verdict.recognizer = source
				break
			}
		}
	}
	return verdict
}

// releaseUsageSources closes every source state.claimed carries. It runs
// after teardown, once the pump has stopped and no drain is still reading
// what a source removes.
func releaseUsageSources(state *sessionState) {
	for _, source := range state.claimed {
		source.Close()
	}
}

// promptQuota is the wire extension a prompt result may carry. It is a
// presence signal and a lower bound, never the accounting figure: it omits
// cache-read and reasoning tokens, and names the serving model wrongly often
// enough that its per-model breakdown is not read here at all.
type promptQuota struct {
	Quota *struct {
		TokenCount struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"token_count"`
	} `json:"quota"`
}

// spendLowerBound returns the input-plus-output figure a prompt result reports
// for its own turn, and whether the extension was present at all. A present
// block reporting zero is a turn that reached no model, which is why presence
// is reported separately from the bound.
func spendLowerBound(meta json.RawMessage) (int64, bool) {
	if len(meta) == 0 {
		return 0, false
	}
	var decoded promptQuota
	if err := json.Unmarshal(meta, &decoded); err != nil || decoded.Quota == nil {
		return 0, false
	}
	count := decoded.Quota.TokenCount
	return max(count.InputTokens, 0) + max(count.OutputTokens, 0), true
}
