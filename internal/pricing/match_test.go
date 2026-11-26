package pricing

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func fixtureTable(t *testing.T, entries ...string) *Table {
	t.Helper()

	members := make([]string, len(entries))
	for i, entry := range entries {
		key, provider, _ := strings.Cut(entry, "=")
		provider = strings.TrimSpace(provider)
		if provider != "" {
			provider = fmt.Sprintf("%q:%q,", "litellm_provider", provider)
		}
		members[i] = fmt.Sprintf(`%q:{%s"input_cost_per_token":1,"output_cost_per_token":2}`, key, provider)
	}
	return mustParse(t, "{"+strings.Join(members, ",")+"}", nil)
}

func TestLookupFixtureTables(t *testing.T) {
	t.Parallel()

	mistral := []string{"mistral-medium-3.5=mistral", "mistral-medium-3-5=mistral"}
	sonnet := []string{"claude-sonnet-4-5=anthropic"}
	tests := []struct {
		name    string
		entries []string
		model   string
		wantKey string
	}{
		{"exact key with a dot", mistral, "mistral-medium-3.5", "mistral-medium-3.5"},
		{"folded form shared by two keys", mistral, "Mistral-Medium-3.5", ""},
		{"case and dotted version folded", []string{"claude-opus-4-6=anthropic"}, "Claude-Opus-4.6", "claude-opus-4-6"},
		{"hyphenated name reaches dotted key", []string{"gpt-5.3-codex=openai"}, "gpt-5-3-codex", "gpt-5.3-codex"},
		{"provider segment and dotted version folded", sonnet, "Anthropic/claude-sonnet-4.5", "claude-sonnet-4-5"},
		{"provider disagrees", sonnet, "openrouter/claude-sonnet-4-5", ""},
		{"provider-less entry is not reached by rule 3", []string{"claude-x"}, "anthropic/claude-x", ""},
		{
			"rule 3 finds two keys",
			[]string{"gemini-2.5-pro=google", "gemini/gemini-2.5-pro=gemini"},
			"google/gemini-2.5-pro",
			"",
		},
		{"placeholder is not a model", []string{"<synthetic>"}, "<synthetic>", ""},
		{"missing cache-write rate stays nil", []string{"gpt-5=openai"}, "gpt-5", "gpt-5"},
		{"empty name", []string{"gpt-5=openai"}, "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			want := TableEntry{}
			if tt.wantKey != "" {
				want = TableEntry{Key: tt.wantKey, InputPerToken: new(1.0), OutputPerToken: new(2.0)}
			}

			got, ok := fixtureTable(t, tt.entries...).Lookup(tt.model)

			if ok != (tt.wantKey != "") || !reflect.DeepEqual(got, want) {
				t.Errorf("Lookup(%q) = %+v, %t, want %+v, %t", tt.model, got, ok, want, tt.wantKey != "")
			}
		})
	}
}
