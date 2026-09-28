package eval

import (
	"slices"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/tools/qualify/evidence"
	"github.com/sortie-ai/sortie/tools/qualify/evidencetest"
	"github.com/sortie-ai/sortie/tools/qualify/profile"
)

func publishedBaseline(T *testing.T, records []evidence.Record, surface evidence.Surface, capability evidence.Capability) evidence.Grade {
	T.Helper()
	path := evidencetest.WriteEvidenceFile(T, records)
	published, err := evidence.ReadEvidenceFile(path)
	if err != nil {
		T.Fatalf("read back the published evidence: %v", err)
	}
	for i := range published {
		if evidence.MatchBaseline(surface, capability)(&published[i]) {
			return published[i].Grade
		}
	}
	T.Fatalf("no published %s %s baseline", surface, capability)
	return ""
}

func noTokenSourceAnywhere(T *testing.T) *evidencetest.Fixture {
	T.Helper()
	fixture := evidencetest.NewFixture(evidencetest.FixtureQualified)
	for _, surface := range []evidence.Surface{evidence.SurfaceProtocol, evidence.SurfaceNativeJSON, evidence.SurfaceNativeStreamJSON} {
		fixture.SetTokenSentinel(surface, false)
	}
	fixture.Finalize()
	return fixture
}

func TestPublishedArtifactAnswersBothQuestions(T *testing.T) {
	T.Parallel()

	fixture := noTokenSourceAnywhere(T)
	report := publishedReport(T, fixture.Records, fixture.Declarations())

	if report.Verdict != evidence.VerdictQualified {
		T.Errorf("transport parity = %s, want qualified: the protocol route loses nothing the native route gave", report.Verdict)
	}
	if report.Conformance != evidence.VerdictNotQualified {
		T.Errorf("product conformance = %s, want not_qualified: the token ceiling works on no route at all", report.Conformance)
	}

	row := rowFor(report, evidence.CapabilityTokenCeiling)
	if row.Standing != StandingSatisfied {
		T.Errorf("token_ceiling parity standing = %s, want satisfied", row.Standing)
	}
	if row.Conformance != StandingBelow {
		T.Errorf("token_ceiling conformance standing = %s, want below", row.Conformance)
	}
	if !strings.Contains(row.ConformanceCause, string(evidence.GradeGap)) {
		T.Errorf("token_ceiling conformance cause = %q, want it to name the shortfall", row.ConformanceCause)
	}
}

func TestSharedShortfallFailsConformanceOnEveryRuntimeShape(T *testing.T) {
	T.Parallel()

	fixture := evidencetest.NewFixture(evidencetest.FixtureQualified,
		profile.AbsentSurface{Surface: evidence.SurfaceNativeJSON, Reason: evidence.SurfaceNotOffered},
		profile.AbsentSurface{Surface: evidence.SurfaceNativeStreamJSON, Reason: evidence.SurfaceNotOffered})
	fixture.SetTokenSentinel(evidence.SurfaceProtocol, false)
	fixture.Finalize()

	report := publishedReport(T, fixture.Records, fixture.Declarations())
	if report.Verdict != evidence.VerdictQualified {
		T.Errorf("transport parity = %s, want qualified: with no native surface there is nothing to be below", report.Verdict)
	}
	if report.Conformance != evidence.VerdictNotQualified {
		T.Errorf("product conformance = %s, want not_qualified: having no native route to lose against is not having the capability", report.Conformance)
	}
}

func TestRemovedObservationCannotImproveConformance(T *testing.T) {
	T.Parallel()

	p := caseLevelProfile(
		profile.SurfaceNotInducible{Surface: evidence.SurfaceProtocol, Case: evidence.CaseLimitReached, Reason: evidence.NotInducibleChannelTooSmall},
		profile.SurfaceNotInducible{Surface: evidence.SurfaceNativeJSON, Case: evidence.CaseLimitReached, Reason: evidence.NotInducibleChannelTooSmall},
		profile.SurfaceNotInducible{Surface: evidence.SurfaceNativeStreamJSON, Case: evidence.CaseLimitReached, Reason: evidence.NotInducibleChannelTooSmall},
	)
	// Set before Finalize so no identity record is written for a session
	// the not-inducible shape removes.
	fixture := evidencetest.NewFixture(evidencetest.FixtureQualified)
	for _, surface := range []evidence.Surface{evidence.SurfaceProtocol, evidence.SurfaceNativeJSON, evidence.SurfaceNativeStreamJSON} {
		fixture.SetSemanticNotInducible(surface, evidence.CapabilityTurnDisposition, evidence.CaseLimitReached, evidence.NotInducibleChannelTooSmall)
	}
	fixture.Finalize()

	row := rowFor(publishedReport(T, fixture.Records, p), evidence.CapabilityTurnDisposition)
	if row.Standing != StandingSatisfied {
		T.Errorf("turn_disposition parity standing = %s (cause %q), want satisfied: no side carries the case", row.Standing, row.Cause)
	}
	if row.Conformance != StandingUnmeasured {
		T.Errorf("turn_disposition conformance standing = %s (cause %q), want unmeasured: nobody induced the case, so nothing supports calling the obligation met", row.Conformance, row.ConformanceCause)
	}
	if !strings.Contains(row.ConformanceCause, string(evidence.CaseLimitReached)) {
		T.Errorf("conformance cause = %q, want it to name the case nobody measured", row.ConformanceCause)
	}
	if !strings.Contains(row.ConformanceCause, evidence.NotInducibleChannelTooSmall) {
		T.Errorf("conformance cause = %q, want it to name why the case went unmeasured: a condition no measurer can reach is not one the surface declined to report", row.ConformanceCause)
	}
}

func TestChannelSilenceIsAConformanceShortfall(T *testing.T) {
	T.Parallel()

	p := caseLevelProfile(profile.SurfaceNotInducible{
		Surface: evidence.SurfaceProtocol,
		Case:    evidence.CaseRuntimeFailure,
		Reason:  evidence.NotInducibleOutputSilentOnFailure,
	})
	fixture := evidencetest.NewFixture(evidencetest.FixtureQualified)
	fixture.SetSemanticNotInducible(evidence.SurfaceProtocol, evidence.CapabilityTurnDisposition, evidence.CaseRuntimeFailure, evidence.NotInducibleOutputSilentOnFailure)
	fixture.Finalize()

	row := rowFor(publishedReport(T, fixture.Records, p), evidence.CapabilityTurnDisposition)
	if row.Conformance != StandingBelow {
		T.Errorf("turn_disposition conformance standing = %s (cause %q), want below: the condition arises and the adapter reports nothing about it", row.Conformance, row.ConformanceCause)
	}
	if !strings.Contains(row.ConformanceCause, evidence.NotInducibleOutputSilentOnFailure) {
		T.Errorf("conformance cause = %q, want it to name the silent channel", row.ConformanceCause)
	}
}

func TestDeclaredGapCarriesNoConformanceObligation(T *testing.T) {
	T.Parallel()

	fixture := evidencetest.NewFixture(evidencetest.FixtureDeclaredGap)
	fixture.Finalize()

	report := publishedReport(T, fixture.Records, fixture.Declarations())
	row := rowFor(report, evidence.CapabilityTurnDisposition)
	if row.Conformance != StandingSatisfied {
		T.Errorf("turn_disposition conformance standing = %s (cause %q), want satisfied: a declared gap is an outcome the runtime never produces", row.Conformance, row.ConformanceCause)
	}
	if report.Conformance != evidence.VerdictQualified {
		T.Errorf("product conformance = %s, want qualified", report.Conformance)
	}
}

func TestCompensationLiftsBothVerdictsWhileTheWireGradeStaysGap(T *testing.T) {
	T.Parallel()

	// A corroboration-only reading is the shape a compensated baseline would
	// flatter: one usable record turns the whole surface usable.
	fixture := evidencetest.NewFixture(evidencetest.FixtureQualified)
	fixture.SetTokenCorroborationOnly(evidence.SurfaceProtocol)
	fixture.Finalize()

	before := publishedReport(T, fixture.Records, fixture.Declarations())
	if before.Verdict != evidence.VerdictNotQualified {
		T.Fatalf("transport parity before compensation = %s, want not_qualified", before.Verdict)
	}
	if before.Conformance != evidence.VerdictNotQualified {
		T.Fatalf("product conformance before compensation = %s, want not_qualified", before.Conformance)
	}
	beforeRow := rowFor(before, evidence.CapabilityTokenCeiling)
	if beforeRow.Standing != StandingBelow || beforeRow.Conformance != StandingBelow {
		T.Fatalf("token_ceiling row before compensation = parity %s / conformance %s, want both below: with no figure reaching Sortie, both questions follow the wire", beforeRow.Standing, beforeRow.Conformance)
	}

	fixture.SetTokenCompensated("sortie/session/turn/usage")

	after := publishedReport(T, fixture.Records, fixture.Declarations())
	if after.Verdict != evidence.VerdictQualified {
		T.Errorf("transport parity = %s, want qualified: a usable compensating record lifts the comparison", after.Verdict)
	}
	if after.Conformance != evidence.VerdictQualified {
		T.Errorf("product conformance = %s, want qualified: the figure reaches Sortie and the default ceiling-stop record grades usable", after.Conformance)
	}
	row := rowFor(after, evidence.CapabilityTokenCeiling)
	if row.Standing != StandingSatisfied {
		T.Errorf("token_ceiling parity standing = %s (cause %q), want satisfied", row.Standing, row.Cause)
	}
	if row.Conformance != StandingSatisfied {
		T.Errorf("token_ceiling conformance standing = %s (cause %q), want satisfied", row.Conformance, row.ConformanceCause)
	}
	if grade := publishedBaseline(T, fixture.Records, evidence.SurfaceProtocol, evidence.CapabilityTokenCeiling); grade != evidence.GradeGap {
		T.Errorf("published protocol token_ceiling baseline = %s, want gap: the baseline states what the surface itself reported", grade)
	}
}

func TestCorroboratingCompensationDoesNotMeetConformanceOrParity(T *testing.T) {
	T.Parallel()

	fixture := evidencetest.NewFixture(evidencetest.FixtureQualified)
	fixture.SetTokenSentinel(evidence.SurfaceProtocol, false)
	fixture.Finalize()
	fixture.SetTokenCompensated("sortie/session/turn/usage")
	compensating := fixture.FindFirst(func(rec *evidence.Record) bool {
		return rec.Scenario == evidence.ScenarioTokenSource && evidence.SuppliedOutsideProtocol(rec.Source)
	})
	if compensating == nil {
		T.Fatal("no compensating token record was written")
	}
	compensating.Grade = evidence.GradeCorroborationOnly

	report := publishedReport(T, fixture.Records, fixture.Declarations())
	if report.Conformance != evidence.VerdictNotQualified {
		T.Errorf("product conformance = %s, want not_qualified: a corroborating reading is not a working ceiling", report.Conformance)
	}
	if report.Verdict != evidence.VerdictNotQualified {
		T.Errorf("transport parity = %s, want not_qualified: a corroborating reading must not lift the comparison either", report.Verdict)
	}
	row := rowFor(report, evidence.CapabilityTokenCeiling)
	if row.Standing != StandingBelow {
		T.Errorf("token_ceiling parity standing = %s (cause %q), want below", row.Standing, row.Cause)
	}
}

func TestCatalogUninducibleCaseCarriesNoConformanceObligation(T *testing.T) {
	T.Parallel()

	fixture := evidencetest.NewFixture(evidencetest.FixtureQualified)
	// Set before Finalize so no identity record is written for a session the
	// not-inducible shape removes.
	for _, surface := range []evidence.Surface{evidence.SurfaceProtocol, evidence.SurfaceNativeJSON, evidence.SurfaceNativeStreamJSON} {
		fixture.SetSemanticNotInducible(surface, evidence.CapabilityRetryClassification, evidence.CaseUnknownOutcome, evidence.NotInducibleDetail)
	}
	fixture.Finalize()

	p := summaryProfile(fixture)
	report := publishedReport(T, fixture.Records, p)
	if report.Conformance != evidence.VerdictQualified {
		T.Errorf("product conformance = %s, want qualified: a catalog-uninducible case carries no obligation", report.Conformance)
	}
	row := rowFor(report, evidence.CapabilityRetryClassification)
	if row.Conformance != StandingSatisfied {
		T.Errorf("retry_classification conformance standing = %s (cause %q), want satisfied", row.Conformance, row.ConformanceCause)
	}

	conclusions, err := conclusionsFromRecords(fixture.Records, report.Conformance, p)
	if err != nil {
		T.Fatalf("conclusionsFromRecords(...) = _, %v, want nil", err)
	}
	wantExcluded := "retry_classification unknown_outcome: no deterministic inducer on any runtime, so the case carries no obligation"
	if !slices.Contains(conclusions.Excluded, wantExcluded) {
		T.Errorf("Excluded = %v, want it to contain %q", conclusions.Excluded, wantExcluded)
	}
}

func TestTokenCeilingConformanceFollowsTheCeilingStopRecordOnlyWhenAFigureReachesSortie(T *testing.T) {
	T.Parallel()

	tests := []struct {
		name              string
		wireGap           bool
		removeCeilingStop bool
		stop              *evidence.Observation
		wantStanding      Standing
		wantCauseContains []string
	}{
		{
			name:              "no figure reaches Sortie, the ceiling stop record is ignored",
			wireGap:           true,
			wantStanding:      StandingBelow,
			wantCauseContains: []string{"protocol gap"},
		},
		{
			name:              "a figure reaches Sortie, the ceiling stop record is missing",
			removeCeilingStop: true,
			wantStanding:      StandingUnmeasured,
			wantCauseContains: []string{"ceiling stop record missing"},
		},
		{
			name: "a figure reaches Sortie, the ceiling stop record is usable",
			stop: &evidence.Observation{
				Grade: evidence.GradeUsable, Outcome: evidence.OutcomePass,
				SessionID: evidencetest.FixtureSession(evidence.SurfaceProtocol, "ceiling"),
				Detail:    "stopped at 1 token(s)",
			},
			wantStanding: StandingSatisfied,
		},
		{
			name: "a figure reaches Sortie, the ceiling stop record is gap",
			stop: &evidence.Observation{
				Grade: evidence.GradeGap, Outcome: evidence.OutcomePass,
				SessionID: evidencetest.FixtureSession(evidence.SurfaceProtocol, "ceiling"),
				Detail:    "the run crossed the ceiling without a stop",
			},
			wantStanding:      StandingBelow,
			wantCauseContains: []string{"ceiling stop gap: the run crossed the ceiling without a stop"},
		},
		{
			name: "a figure reaches Sortie, the ceiling stop record is not_observed",
			stop: &evidence.Observation{
				Grade: evidence.GradeNotObserved, Outcome: evidence.OutcomeRuntimeFailed,
				Detail: "no run under the one-token ceiling ended within the observation bound",
			},
			wantStanding:      StandingUnmeasured,
			wantCauseContains: []string{"ceiling stop runtime_failed: no run under the one-token ceiling ended within the observation bound"},
		},
	}

	for _, tt := range tests {
		T.Run(tt.name, func(T *testing.T) {
			T.Parallel()

			fixture := evidencetest.NewFixture(evidencetest.FixtureQualified)
			if tt.wireGap {
				fixture.SetTokenSentinel(evidence.SurfaceProtocol, false)
			}
			fixture.Finalize()

			switch {
			case tt.removeCeilingStop:
				rec := fixture.FindFirst(matchRowClass(evidence.RowCeilingStop))
				if rec == nil {
					T.Fatal("fixture carries no ceiling stop record to remove")
				}
				fixture.Remove(rec)
			case tt.stop != nil:
				if err := fixture.SetCeilingStop(*tt.stop); err != nil {
					T.Fatalf("SetCeilingStop(...) error = %v, want nil", err)
				}
			}

			report := ExplainEligibility(fixture.Records, fixture.Declarations())
			row := rowFor(report, evidence.CapabilityTokenCeiling)
			if row.Conformance != tt.wantStanding {
				T.Errorf("token_ceiling conformance standing = %s (cause %q), want %s", row.Conformance, row.ConformanceCause, tt.wantStanding)
			}
			for _, want := range tt.wantCauseContains {
				if !strings.Contains(row.ConformanceCause, want) {
					T.Errorf("token_ceiling conformance cause = %q, want it to contain %q", row.ConformanceCause, want)
				}
			}
		})
	}
}

func TestQuestionRationaleIsTotalAndPrescribesNoAbsentIntegration(T *testing.T) {
	T.Parallel()

	for _, question := range evidence.Questions {
		for _, verdict := range evidence.Verdicts {
			line := QuestionRationale(question, verdict)
			if line == "" {
				T.Errorf("QuestionRationale(%s, %s) is empty", question, verdict)
				continue
			}
			for _, banned := range []string{"existing integration", "stays on", "remains on"} {
				if strings.Contains(line, banned) {
					T.Errorf("QuestionRationale(%s, %s) = %q, want no advice to keep an integration that may not exist", question, verdict, line)
				}
			}
		}
	}
	for _, verdict := range evidence.Verdicts {
		if got, want := VerdictRationale(verdict), QuestionRationale(evidence.QuestionTransportParity, verdict); got != want {
			T.Errorf("VerdictRationale(%s) = %q, want the transport parity line %q", verdict, got, want)
		}
	}
}

func TestCompensationIsNotAnInventory(T *testing.T) {
	T.Parallel()

	fixture := evidencetest.NewFixture(evidencetest.FixtureQualified)
	fixture.SetTokenSentinel(evidence.SurfaceProtocol, false)
	fixture.Finalize()
	fixture.SetTokenCompensated("sortie/session/turn/usage")
	fixture.RemoveAll(func(rec *evidence.Record) bool {
		return rec.Scenario == evidence.ScenarioTokenSource && rec.Surface == evidence.SurfaceProtocol &&
			!evidence.SuppliedOutsideProtocol(rec.Source)
	})
	fixture.Renumber()

	path := evidencetest.WriteEvidenceFile(T, fixture.Records)
	_, err := ValidateObservations(path, fixture.Declarations())
	if err == nil {
		T.Fatal("validation accepted a protocol surface whose only token record came from outside the protocol")
	}
	if !strings.Contains(err.Error(), "no token inventory record") {
		T.Errorf("error = %v, want it to name the missing inventory", err)
	}
}

func inventoriedExtension(T *testing.T, reading *evidence.ExtensionReading) *evidencetest.Fixture {
	T.Helper()
	fixture := evidencetest.NewFixture(evidencetest.FixtureQualified)
	err := fixture.SetTokenInventory(evidence.SurfaceProtocol, "", nil,
		evidence.Observation{Grade: evidence.GradeGap, Outcome: evidence.OutcomePass, Detail: "the wire carried no admissible source"},
		reading)
	if err != nil {
		T.Fatalf("SetTokenInventory(protocol, ...) error = %v, want nil", err)
	}
	fixture.Finalize()
	return fixture
}

func protocolInventoryRecord(T *testing.T, records []evidence.Record) evidence.Record {
	T.Helper()
	path := evidencetest.WriteEvidenceFile(T, records)
	published, err := evidence.ReadEvidenceFile(path)
	if err != nil {
		T.Fatalf("read back the published evidence: %v", err)
	}
	for i := range published {
		if published[i].Scenario == evidence.ScenarioTokenSource && published[i].Surface == evidence.SurfaceProtocol {
			return published[i]
		}
	}
	T.Fatal("no published protocol token inventory row")
	return evidence.Record{}
}

func TestPresentButNotAdmittedIsNotAbsent(T *testing.T) {
	T.Parallel()

	partial := inventoriedExtension(T, &evidence.ExtensionReading{Source: evidence.ExtensionSourcePresent})
	none := inventoriedExtension(T, &evidence.ExtensionReading{Source: evidence.ExtensionSourceAbsent})

	partialRow := protocolInventoryRecord(T, partial.Records)
	if partialRow.ExtensionSource == nil || *partialRow.ExtensionSource != evidence.ExtensionSourcePresent {
		T.Errorf("published extension_source = %v, want present", partialRow.ExtensionSource)
	}
	if partialRow.ExtensionAdmitted == nil || *partialRow.ExtensionAdmitted {
		T.Errorf("published extension_admitted = %v, want false", partialRow.ExtensionAdmitted)
	}

	noneRow := protocolInventoryRecord(T, none.Records)
	if noneRow.ExtensionSource == nil || *noneRow.ExtensionSource != evidence.ExtensionSourceAbsent {
		T.Errorf("published extension_source = %v, want absent", noneRow.ExtensionSource)
	}

	partialCause := rowFor(publishedReport(T, partial.Records, partial.Declarations()), evidence.CapabilityTokenCeiling).ConformanceCause
	noneCause := rowFor(publishedReport(T, none.Records, none.Declarations()), evidence.CapabilityTokenCeiling).ConformanceCause
	if partialCause == noneCause {
		T.Errorf("both causes read %q; a source that arrived and cannot be spent is not the absence of one", partialCause)
	}
	if !strings.Contains(partialCause, string(evidence.ExtensionSourcePresent)) {
		T.Errorf("conformance cause = %q, want it to name the source that arrived", partialCause)
	}
}

func TestExtensionReadingBelongsToTheProtocolInventory(T *testing.T) {
	T.Parallel()

	fixture := evidencetest.NewFixture(evidencetest.FixtureQualified)
	fixture.Finalize()
	rec := fixture.FindFirst(evidencetest.MatchSemantic(evidence.SurfaceProtocol, evidence.CapabilityTurnDisposition, evidence.CaseSuccess))
	if rec == nil {
		T.Fatal("no protocol success semantic record")
	}
	rec.ExtensionSource = new(evidence.ExtensionSourcePresent)
	rec.ExtensionAdmitted = new(true)

	path := evidencetest.WriteEvidenceFile(T, fixture.Records)
	_, err := ValidateObservations(path, fixture.Declarations())
	if err == nil || !strings.Contains(err.Error(), "only valid on a protocol token_source record") {
		T.Errorf("error = %v, want a rejection of a reading stated off the inventory", err)
	}
}

func TestExtensionMembersStateEachOther(T *testing.T) {
	T.Parallel()

	for _, tt := range []struct {
		name    string
		mutate  func(rec *evidence.Record)
		wantErr string
	}{
		{
			name:    "admission without its source",
			mutate:  func(rec *evidence.Record) { rec.ExtensionSource = nil },
			wantErr: "requires the extension_source it judges",
		},
		{
			name:    "source without its admission",
			mutate:  func(rec *evidence.Record) { rec.ExtensionAdmitted = nil },
			wantErr: "requires the admission verdict on it",
		},
	} {
		T.Run(tt.name, func(T *testing.T) {
			T.Parallel()
			fixture := inventoriedExtension(T, &evidence.ExtensionReading{Source: evidence.ExtensionSourcePresent})
			rec := fixture.FindFirst(func(rec *evidence.Record) bool {
				return rec.Scenario == evidence.ScenarioTokenSource && rec.Surface == evidence.SurfaceProtocol
			})
			if rec == nil {
				T.Fatal("no protocol token inventory row")
			}
			tt.mutate(rec)

			path := evidencetest.WriteEvidenceFile(T, fixture.Records)
			_, err := ValidateObservations(path, fixture.Declarations())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				T.Errorf("error = %v, want one naming %q", err, tt.wantErr)
			}
		})
	}
}

func TestUnreadSourceCannotBeAdmitted(T *testing.T) {
	T.Parallel()

	fixture := inventoriedExtension(T, &evidence.ExtensionReading{Source: evidence.ExtensionSourcePresent})
	rec := fixture.FindFirst(func(rec *evidence.Record) bool {
		return rec.Scenario == evidence.ScenarioTokenSource && rec.Surface == evidence.SurfaceProtocol
	})
	if rec == nil {
		T.Fatal("no protocol token inventory row")
	}
	rec.ExtensionSource = new(evidence.ExtensionSourceNotObserved)
	rec.ExtensionAdmitted = new(true)

	path := evidencetest.WriteEvidenceFile(T, fixture.Records)
	_, err := ValidateObservations(path, fixture.Declarations())
	if err == nil || !strings.Contains(err.Error(), "cannot be admitted to a budget") {
		T.Errorf("error = %v, want a rejection of an admitted source nobody read", err)
	}
}

func TestNativeSurfaceStatesNoExtensionReading(T *testing.T) {
	T.Parallel()

	fixture := evidencetest.NewFixture(evidencetest.FixtureQualified)
	err := fixture.SetTokenInventory(evidence.SurfaceNativeJSON, "", nil,
		evidence.Observation{Grade: evidence.GradeGap, Outcome: evidence.OutcomePass, Detail: "the native surface carried no source"},
		&evidence.ExtensionReading{Source: evidence.ExtensionSourcePresent, Admitted: true})
	if err == nil {
		T.Fatal("SetTokenInventory(native_json, ...) = nil error, want a rejection")
	}
	if !strings.Contains(err.Error(), "reading of the protocol extension point") {
		T.Errorf("error = %v, want it to name what the native surface cannot state", err)
	}
}

func TestNotApplicableHumanInputDropsOutOfParityAndConformance(T *testing.T) {
	T.Parallel()

	sessionID := evidencetest.FixtureSession(evidence.SurfaceProtocol, "permission")
	protocolObservation := evidence.Observation{
		Grade:        evidence.GradeNotApplicable,
		Outcome:      evidence.OutcomeNotApplicable,
		Detail:       "the request offered a refusing option and was answered inside the protocol, so the turn went on and no human-input outcome arose",
		SessionID:    sessionID,
		EvidencePath: evidence.SemanticEvidencePath(evidence.SurfaceProtocol),
	}

	tests := []struct {
		name          string
		nativeGrade   evidence.Grade
		nativeOutcome evidence.Outcome
		wantStanding  Standing
	}{
		{"native surfaces grade gap", evidence.GradeGap, evidence.OutcomePass, StandingSatisfied},
		{"native surfaces grade usable", evidence.GradeUsable, evidence.OutcomePass, StandingSatisfied},
		{"native surfaces grade not_observed", evidence.GradeNotObserved, evidence.OutcomeFixtureInductionFailed, StandingUnmeasured},
	}

	for _, tc := range tests {
		T.Run(tc.name, func(T *testing.T) {
			T.Parallel()

			fixture := evidencetest.NewFixture(evidencetest.FixtureQualified)
			fixture.Finalize()
			if err := fixture.SetSemanticObservation(evidence.SurfaceProtocol, evidence.CapabilityRetryClassification, evidence.CaseHumanInput, protocolObservation); err != nil {
				T.Fatalf("SetSemanticObservation(protocol human_input) error = %v, want nil", err)
			}
			for _, surface := range []evidence.Surface{evidence.SurfaceNativeJSON, evidence.SurfaceNativeStreamJSON} {
				obs := evidence.Observation{Grade: tc.nativeGrade, Outcome: tc.nativeOutcome, Detail: "human_input case set for the not_applicable comparison control"}
				if tc.nativeGrade != evidence.GradeNotObserved {
					obs.SessionID = evidencetest.FixtureSession(surface, "human-input")
					obs.EvidencePath = evidence.SemanticEvidencePath(surface)
				}
				if err := fixture.SetSemanticObservation(surface, evidence.CapabilityRetryClassification, evidence.CaseHumanInput, obs); err != nil {
					T.Fatalf("SetSemanticObservation(%s human_input) error = %v, want nil", surface, err)
				}
			}

			report := publishedReport(T, fixture.Records, caseLevelProfile())
			row := rowFor(report, evidence.CapabilityRetryClassification)
			if row.Standing != tc.wantStanding {
				T.Errorf("retry_classification standing = %s (cause %q), want %s", row.Standing, row.Cause, tc.wantStanding)
			}
			if row.Conformance != StandingSatisfied || row.ConformanceCause != "" {
				T.Errorf("retry_classification conformance = %s (cause %q), want satisfied with no cause: a not_applicable human_input carries no obligation", row.Conformance, row.ConformanceCause)
			}
		})
	}
}

func TestConformanceCauseAccountsOnlyItsOwnCapability(T *testing.T) {
	T.Parallel()

	fixture := inventoriedExtension(T, &evidence.ExtensionReading{Source: evidence.ExtensionSourcePresent})
	seedRec := fixture.FindFirst(evidencetest.MatchContinuation(evidence.SurfaceProtocol, evidence.InputContinuationSeed))
	if seedRec == nil || seedRec.SessionID == nil {
		T.Fatal("no protocol continuation seed record carrying a session")
	}
	session := *seedRec.SessionID
	err := fixture.SetSessionContinuationObserved(evidence.SurfaceProtocol,
		evidence.Observation{Grade: evidence.GradeUsable, Outcome: evidence.OutcomePass, SessionID: session, Detail: "the seed turn completed and left history"},
		evidence.Observation{Grade: evidence.GradeGap, Outcome: evidence.OutcomePass, SessionID: session, Detail: evidence.RecallSameSessionWithoutRecall})
	if err != nil {
		T.Fatalf("SetSessionContinuationObserved(protocol, ...) error = %v, want nil", err)
	}

	report := publishedReport(T, fixture.Records, fixture.Declarations())

	continuation := rowFor(report, evidence.CapabilitySessionContinuation)
	if continuation.Conformance != StandingBelow {
		T.Fatalf("session_continuation conformance standing = %q, want %q: the control needs the row below the reference", continuation.Conformance, StandingBelow)
	}
	if strings.Contains(continuation.ConformanceCause, "extension") {
		T.Errorf("session_continuation conformance cause = %q, want no extension account: the reading belongs to the rows that took it", continuation.ConformanceCause)
	}

	tokens := rowFor(report, evidence.CapabilityTokenCeiling)
	if !strings.Contains(tokens.ConformanceCause, string(evidence.ExtensionSourcePresent)) {
		T.Errorf("token_ceiling conformance cause = %q, want the extension account its own rows carry", tokens.ConformanceCause)
	}
}
