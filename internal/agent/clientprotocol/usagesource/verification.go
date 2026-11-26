package usagesource

import (
	"strings"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
)

// ClaimVerification returns the registered addition that claims target, with
// its switches, and closes every other claimant; the caller closes the one
// returned.
//
// It returns nothing when none or several claim, and when target or environ
// already sets one of the claimant's switches: replacing an operator's setting
// could change the model, provider, or credential the runtime resolves.
func ClaimVerification(target agentcore.LaunchTarget, environ []string) (VerificationAddition, VerificationSwitches, bool) {
	var claimants []VerificationAddition
	var switches []VerificationSwitches
	for _, kind := range VerificationAdditions.Kinds() {
		constructor, _ := VerificationAdditions.Get(kind) // a listed kind always resolves
		addition := constructor()
		claimed, ok := addition.Claim(target)
		if !ok {
			addition.Close()
			continue
		}
		claimants = append(claimants, addition)
		switches = append(switches, claimed)
	}

	if len(claimants) != 1 || setsSwitch(target, environ, switches[0]) {
		for _, claimant := range claimants {
			claimant.Close()
		}
		return nil, VerificationSwitches{}, false
	}
	return claimants[0], switches[0], true
}

func setsSwitch(target agentcore.LaunchTarget, environ []string, s VerificationSwitches) bool {
	tokens := append([]string{target.Command}, target.Args...)
	for _, arg := range s.Args {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		for _, token := range tokens {
			if token == arg || strings.HasPrefix(token, arg+"=") {
				return true
			}
		}
	}
	for _, entry := range s.Env {
		name, _, _ := strings.Cut(entry, "=")
		for _, set := range environ {
			setName, _, _ := strings.Cut(set, "=")
			if strings.EqualFold(setName, name) {
				return true
			}
		}
	}
	return false
}
