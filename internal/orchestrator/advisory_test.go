package orchestrator

import (
	"log/slog"
	"testing"

	"github.com/sortie-ai/sortie/internal/config"
)

func TestAdvisoryEqual(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a    config.Advisory
		b    config.Advisory
		want bool
	}{
		{
			name: "equal Message and Attrs, differing Check and Text, are the same advisory",
			a:    config.Advisory{Check: "check.a", Text: "text a", Message: "m", Attrs: []slog.Attr{slog.String("k", "v")}},
			b:    config.Advisory{Check: "check.b", Text: "text b", Message: "m", Attrs: []slog.Attr{slog.String("k", "v")}},
			want: true,
		},
		{
			name: "differing Message breaks identity",
			a:    config.Advisory{Message: "m1"},
			b:    config.Advisory{Message: "m2"},
			want: false,
		},
		{
			name: "differing Attrs key or value breaks identity",
			a:    config.Advisory{Message: "m", Attrs: []slog.Attr{slog.String("k1", "v")}},
			b:    config.Advisory{Message: "m", Attrs: []slog.Attr{slog.String("k2", "v")}},
			want: false,
		},
		{
			name: "differing Attrs order breaks identity",
			a:    config.Advisory{Message: "m", Attrs: []slog.Attr{slog.String("a", "1"), slog.String("b", "2")}},
			b:    config.Advisory{Message: "m", Attrs: []slog.Attr{slog.String("b", "2"), slog.String("a", "1")}},
			want: false,
		},
		{
			name: "differing Attrs length breaks identity",
			a:    config.Advisory{Message: "m", Attrs: []slog.Attr{slog.String("a", "1")}},
			b:    config.Advisory{Message: "m", Attrs: []slog.Attr{slog.String("a", "1"), slog.String("b", "2")}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := advisoryEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("advisoryEqual(%+v, %+v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestNewAdvisories(t *testing.T) {
	t.Parallel()

	a := config.Advisory{Message: "a"}
	b := config.Advisory{Message: "b"}
	c := config.Advisory{Message: "c"}

	tests := []struct {
		name     string
		current  []config.Advisory
		previous []config.Advisory
		want     []config.Advisory
	}{
		{
			name:     "empty previous reports every member of current, in order",
			current:  []config.Advisory{a, b},
			previous: nil,
			want:     []config.Advisory{a, b},
		},
		{
			name:     "only the member absent from previous is reported, an unchanged member is not",
			current:  []config.Advisory{a, b, c},
			previous: []config.Advisory{a, c},
			want:     []config.Advisory{b},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := newAdvisories(tt.current, tt.previous)
			if len(got) != len(tt.want) {
				t.Fatalf("newAdvisories(%+v, %+v) = %+v, want %+v", tt.current, tt.previous, got, tt.want)
			}
			for i := range tt.want {
				if got[i].Message != tt.want[i].Message {
					t.Errorf("newAdvisories(...)[%d].Message = %q, want %q", i, got[i].Message, tt.want[i].Message)
				}
			}
		})
	}
}
