package usage_test

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/agent/usage"
	"github.com/sortie-ai/sortie/internal/domain"
)

var observedAt = time.Date(2026, 10, 28, 14, 0, 0, 0, time.FixedZone("UTC+3", 3*60*60))

func declaredSource(unit usage.ShareUnit) usage.Source {
	return usage.Source{Windows: usage.WindowDeclaration{
		Share: unit,
		Statuses: map[string]domain.UsageWindowStatus{
			"allowed":         domain.UsageWindowAllowed,
			"allowed_warning": domain.UsageWindowWarning,
			"rejected":        domain.UsageWindowExhausted,
		},
		Spans: map[string]usage.Span{
			"five_hour":    {Minutes: 300},
			"seven_day":    {Minutes: 10080},
			"seven_day_pw": {Minutes: 10080, Scope: "Opus"},
			"overage":      {Scope: "extra usage"},
		},
	}}
}

func remainingSource(unit usage.ShareUnit) usage.Source {
	source := declaredSource(unit)
	source.Windows.ShareRemaining = true
	return source
}

func creditsSource(unit usage.ShareUnit) usage.Source {
	source := declaredSource(unit)
	source.Windows.Units = map[string]string{"five_hour": "credits"}
	return source
}

func TestWindows_NormalizesOneReading(t *testing.T) {
	t.Parallel()

	fraction, percent := declaredSource(usage.ShareFraction), declaredSource(usage.SharePercent)
	tests := []struct {
		name        string
		source      usage.Source
		reading     usage.WindowReading
		wantKey     string
		wantLabel   string
		wantMinutes int64
		wantUsed    *float64
		wantStatus  domain.UsageWindowStatus
		wantReset   time.Time
		wantAmount  *domain.UsageWindowAmount
		wantAt      time.Time
	}{
		{name: "fraction scales to percent", source: fraction, reading: usage.WindowReading{Window: "five_hour", Share: new(0.9)}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300, wantUsed: new(float64(90))},
		{name: "fraction above the whole is not clamped", source: fraction, reading: usage.WindowReading{Window: "five_hour", Share: new(1.04)}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300, wantUsed: new(float64(104))},
		{name: "percent is kept as reported", source: percent, reading: usage.WindowReading{Window: "five_hour", Share: new(float64(38))}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300, wantUsed: new(float64(38))},
		{name: "absent share is unknown", source: fraction, reading: usage.WindowReading{Window: "five_hour"}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300},
		{name: "negative share is unknown", source: fraction, reading: usage.WindowReading{Window: "five_hour", Share: new(-0.1)}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300},
		{name: "NaN share is unknown", source: fraction, reading: usage.WindowReading{Window: "five_hour", Share: new(math.NaN())}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300},
		{name: "infinite share is unknown", source: fraction, reading: usage.WindowReading{Window: "five_hour", Share: new(math.Inf(1))}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300},
		{name: "undeclared unit leaves the share unread", source: declaredSource(usage.ShareUndeclared), reading: usage.WindowReading{Window: "five_hour", Share: new(0.9)}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300},
		{name: "allowed status", source: fraction, reading: usage.WindowReading{Window: "five_hour", Status: "allowed"}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300, wantStatus: domain.UsageWindowAllowed},
		{name: "warning status", source: fraction, reading: usage.WindowReading{Window: "five_hour", Status: "allowed_warning"}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300, wantStatus: domain.UsageWindowWarning},
		{name: "exhausted status", source: fraction, reading: usage.WindowReading{Window: "five_hour", Status: "rejected"}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300, wantStatus: domain.UsageWindowExhausted},
		{name: "unknown status word is unknown", source: fraction, reading: usage.WindowReading{Window: "five_hour", Status: "throttled"}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300},
		{name: "source without statuses leaves status unread", source: usage.Source{Windows: usage.WindowDeclaration{Share: usage.SharePercent}}, reading: usage.WindowReading{Status: "allowed"}, wantLabel: "Usage limit"},
		{name: "far-future reset", source: fraction, reading: usage.WindowReading{Window: "five_hour", ResetsAt: 4102444800}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300, wantReset: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)},
		{name: "zero reset is unknown", source: fraction, reading: usage.WindowReading{Window: "five_hour", ResetsAt: 0}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300},
		{name: "negative reset is unknown", source: fraction, reading: usage.WindowReading{Window: "five_hour", ResetsAt: -5}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300},
		{name: "declared span wins over the reading's own", source: fraction, reading: usage.WindowReading{Window: "seven_day", Span: usage.Span{Minutes: 300}}, wantKey: "seven_day", wantLabel: "Weekly limit", wantMinutes: 10080},
		{name: "declared span with scope", source: fraction, reading: usage.WindowReading{Window: "seven_day_pw"}, wantKey: "seven_day_pw", wantLabel: "Weekly Opus limit", wantMinutes: 10080},
		{name: "scope without length", source: fraction, reading: usage.WindowReading{Window: "overage"}, wantKey: "overage", wantLabel: "Extra usage limit"},
		{name: "reading's own span when identifier is undeclared", source: fraction, reading: usage.WindowReading{Window: "codex/primary", Span: usage.Span{Minutes: 1440}}, wantKey: "codex/primary", wantLabel: "Daily limit", wantMinutes: 1440},
		{name: "month-long span", source: fraction, reading: usage.WindowReading{Span: usage.Span{Minutes: 43200}}, wantLabel: "30-day limit", wantMinutes: 43200},
		{name: "length that is not a whole hour", source: fraction, reading: usage.WindowReading{Span: usage.Span{Minutes: 90}}, wantLabel: "90-minute limit", wantMinutes: 90},
		{name: "span scope is trimmed and its first rune upper-cased", source: fraction, reading: usage.WindowReading{Span: usage.Span{Minutes: 10080, Scope: "  opus "}}, wantLabel: "Weekly opus limit", wantMinutes: 10080},
		{name: "identifier outside the vocabulary and no span", source: fraction, reading: usage.WindowReading{Window: "monthly"}, wantKey: "monthly", wantLabel: "Usage limit"},
		{name: "no identifier and no span", source: fraction, reading: usage.WindowReading{}, wantLabel: "Usage limit"},
		{name: "remaining fraction serves used percent", source: remainingSource(usage.ShareFraction), reading: usage.WindowReading{Window: "five_hour", Share: new(0.25)}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300, wantUsed: new(float64(75))},
		{name: "remaining percent serves used percent", source: remainingSource(usage.SharePercent), reading: usage.WindowReading{Window: "five_hour", Share: new(37.5)}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300, wantUsed: new(62.5)},
		{name: "remaining above the whole is unknown", source: remainingSource(usage.ShareFraction), reading: usage.WindowReading{Window: "five_hour", Share: new(1.2)}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300},
		{name: "negative remaining is above the whole used", source: remainingSource(usage.ShareFraction), reading: usage.WindowReading{Window: "five_hour", Share: new(-0.04)}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300, wantUsed: new(float64(104))},
		{name: "amount serves itself and its ratio", source: creditsSource(usage.ShareUndeclared), reading: usage.WindowReading{Window: "five_hour", Used: new(7.08), Limit: new(float64(1000))}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300, wantUsed: new(0.708), wantAmount: &domain.UsageWindowAmount{Used: 7.08, Limit: 1000, Unit: "credits"}},
		{name: "declared share wins over the amount ratio", source: creditsSource(usage.SharePercent), reading: usage.WindowReading{Window: "five_hour", Share: new(float64(50)), Used: new(7.08), Limit: new(float64(1000))}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300, wantUsed: new(float64(50)), wantAmount: &domain.UsageWindowAmount{Used: 7.08, Limit: 1000, Unit: "credits"}},
		{name: "no amount without a declared unit", source: declaredSource(usage.ShareUndeclared), reading: usage.WindowReading{Window: "five_hour", Used: new(7.08), Limit: new(float64(1000))}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300},
		{name: "no amount with a zero limit", source: creditsSource(usage.ShareUndeclared), reading: usage.WindowReading{Window: "five_hour", Used: new(7.08), Limit: new(float64(0))}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300},
		{name: "no amount with NaN", source: creditsSource(usage.ShareUndeclared), reading: usage.WindowReading{Window: "five_hour", Used: new(math.NaN()), Limit: new(float64(1000))}, wantKey: "five_hour", wantLabel: "5-hour limit", wantMinutes: 300},
		{name: "monthly period without scope", source: fraction, reading: usage.WindowReading{Span: usage.Span{Period: "Monthly"}}, wantLabel: "Monthly limit"},
		{name: "monthly period with scope", source: fraction, reading: usage.WindowReading{Span: usage.Span{Period: "Monthly", Scope: "premium request"}}, wantLabel: "Monthly premium request limit"},
		{name: "rolling period", source: fraction, reading: usage.WindowReading{Span: usage.Span{Period: "Rolling"}}, wantLabel: "Rolling limit"},
		{name: "scope without length is free usage", source: fraction, reading: usage.WindowReading{Span: usage.Span{Scope: "free usage"}}, wantLabel: "Free usage limit"},
		{name: "scope names a model", source: fraction, reading: usage.WindowReading{Span: usage.Span{Scope: "gemini-2.5-pro"}}, wantLabel: "Gemini-2.5-pro limit"},
		{name: "reset delay counts from the observation", source: fraction, reading: usage.WindowReading{ObservedAt: observedAt.Unix() - 600, ResetsAfter: time.Hour}, wantLabel: "Usage limit", wantReset: observedAt.UTC().Add(-10*time.Minute + time.Hour), wantAt: observedAt.UTC().Add(-10 * time.Minute)},
		{name: "observation before the report is the report time", source: fraction, reading: usage.WindowReading{ObservedAt: observedAt.Unix() - 600}, wantLabel: "Usage limit", wantAt: observedAt.UTC().Add(-10 * time.Minute)},
		{name: "observation after the report is not the report time", source: fraction, reading: usage.WindowReading{ObservedAt: observedAt.Unix() + 600}, wantLabel: "Usage limit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := usage.Windows(tt.source, []usage.WindowReading{tt.reading}, observedAt)

			if len(got) != 1 {
				t.Fatalf("Windows(%+v) = %d windows, want 1", tt.reading, len(got))
			}
			w := got[0]
			if w.Key != tt.wantKey || w.Label != tt.wantLabel || w.Minutes != tt.wantMinutes || w.Status != tt.wantStatus {
				t.Errorf("Windows(%+v) = {Key %q, Label %q, Minutes %d, Status %q}, want {%q, %q, %d, %q}", tt.reading, w.Key, w.Label, w.Minutes, w.Status, tt.wantKey, tt.wantLabel, tt.wantMinutes, tt.wantStatus)
			}
			if (w.UsedPercent == nil) != (tt.wantUsed == nil) || (w.UsedPercent != nil && math.Abs(*w.UsedPercent-*tt.wantUsed) > 1e-9) {
				t.Errorf("Windows(%+v).UsedPercent = %v, want %v", tt.reading, w.UsedPercent, tt.wantUsed)
			}
			if !w.ResetsAt.Equal(tt.wantReset) || (!w.ResetsAt.IsZero() && w.ResetsAt.Location() != time.UTC) {
				t.Errorf("Windows(%+v).ResetsAt = %v, want %v in UTC", tt.reading, w.ResetsAt, tt.wantReset)
			}
			wantAt := tt.wantAt
			if wantAt.IsZero() {
				wantAt = observedAt.UTC()
			}
			if !w.ReportedAt.Equal(wantAt) || w.ReportedAt.Location() != time.UTC {
				t.Errorf("Windows(%+v).ReportedAt = %v, want %v in UTC", tt.reading, w.ReportedAt, wantAt)
			}
			if (w.Amount == nil) != (tt.wantAmount == nil) || (w.Amount != nil && *w.Amount != *tt.wantAmount) {
				t.Errorf("Windows(%+v).Amount = %v, want %v", tt.reading, w.Amount, tt.wantAmount)
			}
		})
	}
}

func TestWindows_KeepsIdentifiersApartAndOrdered(t *testing.T) {
	t.Parallel()

	source := usage.Source{Windows: usage.WindowDeclaration{
		Share: usage.SharePercent,
		Spans: map[string]usage.Span{"seven_day": {Minutes: 10080}, "seven_day_overage_included": {Minutes: 10080}},
	}}
	readings := []usage.WindowReading{{Window: "seven_day_overage_included"}, {Window: "seven_day"}}

	got := usage.Windows(source, readings, observedAt)

	keys := []string{}
	for _, w := range got {
		keys = append(keys, w.Key+"="+w.Label)
	}
	want := []string{"seven_day_overage_included=Weekly limit", "seven_day=Weekly limit"}
	if !slices.Equal(keys, want) {
		t.Errorf("Windows(two weekly identifiers) = %q, want %q in reading order", keys, want)
	}
	if got := usage.Windows(source, nil, observedAt); got != nil {
		t.Errorf("Windows(no readings) = %+v, want nil", got)
	}
}

func window(key, label string, minutes int64, reported time.Time) domain.UsageWindow {
	return domain.UsageWindow{Key: key, Label: label, Minutes: minutes, ReportedAt: reported}
}

func describe(windows []domain.UsageWindow) []string {
	described := []string{}
	for _, w := range windows {
		described = append(described, w.Key+"/"+w.Label+"@"+w.ReportedAt.Format("15:04"))
	}
	return described
}

func TestMergeWindows(t *testing.T) {
	t.Parallel()

	at := func(hour int) time.Time { return time.Date(2026, 10, 28, hour, 0, 0, 0, time.UTC) }
	tests := []struct {
		name     string
		held     []domain.UsageWindow
		reported []domain.UsageWindow
		want     []string
	}{
		{
			name:     "report replaces only the key it carries",
			held:     []domain.UsageWindow{window("a", "Weekly limit", 10080, at(10)), window("b", "5-hour limit", 300, at(10))},
			reported: []domain.UsageWindow{window("b", "5-hour limit", 300, at(12))},
			want:     []string{"b/5-hour limit@12:00", "a/Weekly limit@10:00"},
		},
		{
			name:     "held window the report omits stays",
			held:     []domain.UsageWindow{window("a", "Weekly limit", 10080, at(10))},
			reported: []domain.UsageWindow{window("b", "5-hour limit", 300, at(12))},
			want:     []string{"b/5-hour limit@12:00", "a/Weekly limit@10:00"},
		},
		{
			name:     "earlier report never replaces a later one",
			held:     []domain.UsageWindow{window("a", "new", 300, at(12))},
			reported: []domain.UsageWindow{window("a", "old", 300, at(10))},
			want:     []string{"a/new@12:00"},
		},
		{
			name:     "report at the same instant replaces",
			held:     []domain.UsageWindow{window("a", "held", 300, at(10))},
			reported: []domain.UsageWindow{window("a", "reported", 300, at(10))},
			want:     []string{"a/reported@10:00"},
		},
		{
			name:     "no held windows",
			reported: []domain.UsageWindow{window("a", "5-hour limit", 300, at(10))},
			want:     []string{"a/5-hour limit@10:00"},
		},
		{
			name: "order is length ascending with unknown length last, then label, then key",
			held: []domain.UsageWindow{
				window("z", "Usage limit", 0, at(10)),
				window("w", "Weekly limit", 10080, at(10)),
				window("k2", "Same limit", 300, at(10)),
				window("k1", "Same limit", 300, at(10)),
				window("l", "Another limit", 300, at(10)),
			},
			reported: []domain.UsageWindow{window("a", "Extra usage limit", 0, at(10))},
			want: []string{
				"l/Another limit@10:00", "k1/Same limit@10:00", "k2/Same limit@10:00",
				"w/Weekly limit@10:00", "a/Extra usage limit@10:00", "z/Usage limit@10:00",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			heldBefore := describe(tt.held)

			got := describe(usage.MergeWindows(tt.held, tt.reported))

			if !slices.Equal(got, tt.want) {
				t.Errorf("MergeWindows() = %q, want %q", got, tt.want)
			}
			if after := describe(tt.held); !slices.Equal(after, heldBefore) {
				t.Errorf("MergeWindows() changed held to %q, want %q untouched", after, heldBefore)
			}
		})
	}
}

func TestCurrentWindows(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC)
	resetting := func(key string, resetsAt time.Time) domain.UsageWindow {
		return domain.UsageWindow{Key: key, ResetsAt: resetsAt}
	}
	keysOf := func(windows []domain.UsageWindow) []string {
		keys := []string{}
		for _, w := range windows {
			keys = append(keys, w.Key)
		}
		return keys
	}

	got := usage.CurrentWindows([]domain.UsageWindow{
		resetting("past", now.Add(-time.Second)),
		resetting("unknown", time.Time{}),
		resetting("now", now),
		resetting("future", now.Add(time.Second)),
	}, now)

	if want := []string{"unknown", "future"}; !slices.Equal(keysOf(got), want) {
		t.Errorf("CurrentWindows() keys = %q, want %q", keysOf(got), want)
	}
	if all := usage.CurrentWindows([]domain.UsageWindow{resetting("past", now.Add(-time.Hour))}, now); all == nil || len(all) != 0 {
		t.Errorf("CurrentWindows(all reset) = %#v, want an empty non-nil slice", all)
	}
}
