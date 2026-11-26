package pricing

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const baselineTemplate = `{
  "sample_spec": {"input_cost_per_token": "0.0"},
  "fallback_generalizations": {"rules": [{"match": "x"}]},
  "documentation": "https://example.test",
  "model-a": {"litellm_provider": @PROVIDER@, "input_cost_per_token": @RATE@, "output_cost_per_token": 0.000002, "mode": "chat"},
  "model-b": {@EXTRA@"litellm_provider": "anthropic", "input_cost_per_token": 0.000003, "cache_read_input_token_cost": 3e-7},
  "no-rate": {"litellm_provider": "openai", "mode": "embedding"}
}`

func baseline(overrides ...string) string {
	defaults := []string{"@PROVIDER@", `"openai"`, "@RATE@", "0.000001", "@EXTRA@", ""}
	return strings.NewReplacer(append(overrides, defaults...)...).Replace(baselineTemplate)
}

func mustParse(t *testing.T, data string, previous *Table) *Table {
	t.Helper()

	tbl, err := ParseTable([]byte(data), previous)
	if err != nil {
		t.Fatalf("ParseTable() error = %v, want nil", err)
	}
	return tbl
}

func tableWithRated(t *testing.T, n int) *Table {
	t.Helper()

	members := make([]string, n)
	for i := range members {
		members[i] = fmt.Sprintf(`"p%d":{"input_cost_per_token":1}`, i)
	}
	return mustParse(t, "{"+strings.Join(members, ",")+"}", nil)
}

func TestParseTableValidation(t *testing.T) {
	t.Parallel()

	valid := baseline()
	tests := []struct {
		name     string
		data     string
		previous *Table
		wantErr  bool
	}{
		{"baseline with skipped members", valid, nil, false},
		{"truncated before closing brace", strings.TrimSuffix(strings.TrimSpace(valid), "}"), nil, true},
		{"negative rate", baseline("@RATE@", "-0.000001"), nil, true},
		{"rate as string", baseline("@RATE@", `"0.000015"`), nil, true},
		{"provider as number", baseline("@PROVIDER@", "7"), nil, true},
		{"array", "[]", nil, true},
		{"empty object", "{}", nil, true},
		{"entries without a rate", `{"model-a": {"litellm_provider": "openai", "mode": "chat"}}`, nil, true},
		{"previous holds 2B+1 rated entries", valid, tableWithRated(t, 5), true},
		{"previous holds 2B rated entries", valid, tableWithRated(t, 4), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseTable([]byte(tt.data), tt.previous)

			if tt.wantErr {
				if err == nil || got != nil {
					t.Fatalf("ParseTable(%s) = (nil table: %t), %v, want nil table and error", tt.name, got == nil, err)
				}
				return
			}
			if err != nil || got == nil {
				t.Fatalf("ParseTable(%s) = (nil table: %t), %v, want table and nil error", tt.name, got == nil, err)
			}
		})
	}
}

func TestParseTableZeroRateRule(t *testing.T) {
	t.Parallel()

	withModel := func(entry string) string {
		doc := `{"filler":{"input_cost_per_token":1}`
		if entry != "" {
			doc += `,"m":` + entry
		}
		return doc + "}"
	}
	tests := []struct {
		name         string
		previous     string
		candidate    string
		wantProvider string
		want         TableEntry
	}{
		{
			"rate fell to zero keeps the whole previous entry",
			`{"litellm_provider":"anthropic","input_cost_per_token":5,"output_cost_per_token":7}`,
			`{"litellm_provider":"openai","input_cost_per_token":0,"output_cost_per_token":9}`,
			"anthropic",
			TableEntry{Key: "m", InputPerToken: new(5.0), OutputPerToken: new(7.0)},
		},
		{
			"zero over zero keeps the candidate",
			`{"litellm_provider":"anthropic","input_cost_per_token":0}`,
			`{"litellm_provider":"openai","input_cost_per_token":0,"output_cost_per_token":9}`,
			"openai",
			TableEntry{Key: "m", InputPerToken: new(0.0), OutputPerToken: new(9.0)},
		},
		{
			"no previous entry keeps the candidate",
			"",
			`{"litellm_provider":"openai","input_cost_per_token":0}`,
			"openai",
			TableEntry{Key: "m", InputPerToken: new(0.0)},
		},
		{
			"previous entry without a rate keeps the candidate",
			`{"litellm_provider":"anthropic"}`,
			`{"litellm_provider":"openai","input_cost_per_token":0}`,
			"openai",
			TableEntry{Key: "m", InputPerToken: new(0.0)},
		},
		{
			"rate that disappeared keeps the candidate",
			`{"litellm_provider":"anthropic","input_cost_per_token":5}`,
			`{"litellm_provider":"openai","output_cost_per_token":9}`,
			"openai",
			TableEntry{Key: "m", OutputPerToken: new(9.0)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			previous := mustParse(t, withModel(tt.previous), nil)
			table := mustParse(t, withModel(tt.candidate), previous)

			got, ok := table.Lookup(tt.wantProvider + "/m")

			if !ok || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Lookup(%q) = %+v, %t, want %+v, true", tt.wantProvider+"/m", got, ok, tt.want)
			}
		})
	}
}

func TestTableVersion(t *testing.T) {
	t.Parallel()

	base := mustParse(t, baseline(), nil)
	reordered := `{"no-rate":{"mode":"embedding","litellm_provider":"openai"},` +
		`"model-b":{"cache_read_input_token_cost":3e-7,"input_cost_per_token":0.000003,"litellm_provider":"anthropic"},` +
		`"model-a":{"output_cost_per_token":0.000002,"input_cost_per_token":0.000001,"litellm_provider":"openai"}}`
	tests := []struct {
		name     string
		data     string
		wantSame bool
	}{
		{"reindented", strings.ReplaceAll(baseline(), "  ", "\t"), true},
		{"members reordered", reordered, true},
		{"unread field added", baseline("@EXTRA@", `"max_tokens": "n/a", `), true},
		{"one rate changed", baseline("@RATE@", "0.000002"), false},
	}

	if !strings.HasPrefix(base.Version(), "sha256:") || len(base.Version()) != len("sha256:")+64 || !base.AsOf().IsZero() {
		t.Errorf("ParseTable(baseline) Version() = %q, AsOf() = %v, want sha256:<64 hex> and zero time", base.Version(), base.AsOf())
	}

	reparsed := mustParse(t, string(base.Encode()), nil)
	if reparsed.Version() != base.Version() {
		t.Errorf("ParseTable(Encode()).Version() = %q, want %q", reparsed.Version(), base.Version())
	}
	for _, name := range []string{"model-a", "model-b", "no-rate"} {
		got, gotOK := reparsed.Lookup(name)
		want, wantOK := base.Lookup(name)
		if gotOK != wantOK || !reflect.DeepEqual(got, want) {
			t.Errorf("reparsed Lookup(%q) = %+v, %t, want %+v, %t", name, got, gotOK, want, wantOK)
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := mustParse(t, tt.data, nil).Version()

			if (got == base.Version()) != tt.wantSame {
				t.Errorf("ParseTable(%s).Version() = %q, baseline %q, want same = %t", tt.name, got, base.Version(), tt.wantSame)
			}
		})
	}
}
