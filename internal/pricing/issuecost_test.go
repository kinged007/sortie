package pricing

import "testing"

func TestIssueCost(t *testing.T) {
	t.Parallel()

	usd := func(v float64) *float64 { return &v }

	tests := []struct {
		name          string
		recorded      RecordedCost
		session       SessionCost
		spendComplete bool
		wantUSD       float64
		wantStatus    CostStatus
	}{
		{name: "unpriced run beside a priced run yields the priced sum, not estimated", recorded: RecordedCost{USD: 0.5, Priced: 1, Unpriced: 1}, spendComplete: true, wantUSD: 0.5, wantStatus: CostPartial},
		{name: "priced runs and a priced session are estimated and summed", recorded: RecordedCost{USD: 0.5, Priced: 1}, session: SessionCost{USD: usd(0.25), Status: CostEstimated, Spent: true}, spendComplete: true, wantUSD: 0.75, wantStatus: CostEstimated},
		{name: "a partial recorded run is partial even when spend is complete", recorded: RecordedCost{USD: 0.5, Priced: 1, Partial: 1}, spendComplete: true, wantUSD: 0.5, wantStatus: CostPartial},
		{name: "priced runs with incomplete spend are partial", recorded: RecordedCost{USD: 0.5, Priced: 1}, wantUSD: 0.5, wantStatus: CostPartial},
		{name: "an unpriced session that spent makes a fully priced issue partial", recorded: RecordedCost{USD: 0.5, Priced: 1}, session: SessionCost{Status: CostUnpriced, Spent: true}, spendComplete: true, wantUSD: 0.5, wantStatus: CostPartial},
		{name: "an unpriced session that spent nothing leaves a fully priced issue estimated", recorded: RecordedCost{USD: 0.5, Priced: 1}, session: SessionCost{Status: CostUnpriced}, spendComplete: true, wantUSD: 0.5, wantStatus: CostEstimated},
		{name: "only unpriced spend is unpriced with a zero sum", recorded: RecordedCost{Unpriced: 2}, spendComplete: true, wantStatus: CostUnpriced},
		{name: "incomplete spend with nothing priced or unpriced is unmeasured", wantStatus: CostUnmeasured},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotUSD, gotStatus := IssueCost(tt.recorded, tt.session, tt.spendComplete)

			if gotUSD != tt.wantUSD || gotStatus != tt.wantStatus {
				t.Errorf("IssueCost(%+v, %+v, %v) = (%v, %q), want (%v, %q)",
					tt.recorded, tt.session, tt.spendComplete, gotUSD, gotStatus, tt.wantUSD, tt.wantStatus)
			}
		})
	}
}

func TestCeilingReached(t *testing.T) {
	t.Parallel()

	// A variable sum: a constant expression 0.7 + 0.1 would be folded
	// exactly to 0.8 and never exercise the float error.
	sum := func(a, b float64) float64 { return a + b }

	tests := []struct {
		name    string
		usd     float64
		ceiling float64
		want    bool
	}{
		{name: "float sum equal to the ceiling is reached", usd: sum(0.7, 0.1), ceiling: 0.8, want: true},
		{name: "one cent below the ceiling is not reached", usd: sum(0.7, 0.09), ceiling: 0.8, want: false},
		{name: "past the ceiling is reached", usd: 1.25, ceiling: 0.8, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := CeilingReached(tt.usd, tt.ceiling); got != tt.want {
				t.Errorf("CeilingReached(%v, %v) = %v, want %v", tt.usd, tt.ceiling, got, tt.want)
			}
		})
	}
}

func TestFormatUSD(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   float64
		want string
	}{
		{0.5, "$0.50"},
		{5, "$5.00"},
		{1234.56, "$1,234.56"},
		{999.999, "$1,000.00"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()

			if got := FormatUSD(tt.in); got != tt.want {
				t.Errorf("FormatUSD(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
