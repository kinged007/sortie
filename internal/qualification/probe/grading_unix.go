//go:build unix

package probe

import "github.com/sortie-ai/sortie/internal/qualification"

// inducedRow is one collector-driven observation grade and its detail,
// passed to gradedEvidence for one of the three rows Run's own
// inducers grade.
type inducedRow struct {
	grade  qualification.Grade
	detail string
}

// gradedEvidence builds the evidence one live collection publishes: a
// not-observed fixture for profile's own declared and absent surfaces,
// with every declaration applied, and exactly the three
// collector-driven rows rewritten to the grade and detail each
// inducer observed: tool server delivery, permission handling, and
// protocol session continuation. It launches nothing and reads no
// environment.
func gradedEvidence(profile qualification.RuntimeProfile, toolServer, permission, continuation inducedRow) *qualification.Fixture {
	fixture := qualification.NewFixture(qualification.FixtureNotObserved, profile.AbsentSurfaces...)
	for _, declaration := range profile.Declarations {
		fixture.SetSemanticDeclaredGap(declaration.Capability, declaration.Case, declaration.Reason)
	}
	fixture.SetToolServerDelivery(toolServer.grade, toolServer.detail)
	fixture.SetPermissionHandling(permission.grade, permission.detail)
	fixture.SetSessionContinuation(qualification.SurfaceProtocol, continuation.grade, continuation.detail)
	fixture.Finalize()
	return fixture
}
