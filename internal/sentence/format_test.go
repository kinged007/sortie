package sentence

import (
	"strings"
	"testing"
)

func TestFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		call string
		got  string
		want string
	}{
		{"duration(0.4)", duration(0.4), "under 1s"},
		{"duration(3.66)", duration(3.66), "3.7s"},
		{"duration(9.96)", duration(9.96), "10s"},
		{"duration(59.4)", duration(59.4), "59s"},
		{"duration(59.6)", duration(59.6), "1m"},
		{"duration(3569)", duration(3569), "59m"},
		{"duration(3570)", duration(3570), "1h"},
		{"duration(4440)", duration(4440), "1h 14m"},
		{"duration(86400)", duration(86400), "1d"},
		{"duration(97200)", duration(97200), "1d 3h"},
		{"due(0.5)", due(0.5), "due now"},
		{"due(30)", due(30), "in 30s"},
		{"tokens(999)", tokens(new(int64(999))), "999"},
		{"tokens(1000)", tokens(new(int64(1000))), "1K"},
		{"tokens(704000)", tokens(new(int64(704000))), "704K"},
		{"tokens(999499)", tokens(new(int64(999499))), "999K"},
		{"tokens(999500)", tokens(new(int64(999500))), "1M"},
		{"tokens(2020000)", tokens(new(int64(2020000))), "2.02M"},
		{"tokens(nil)", tokens(nil), "not measured"},
		{"count(27102)", count(27102), "27,102"},
		{"plural(1)", plural(1, "failing check", "failing checks"), "1 failing check"},
		{"plural(2)", plural(2, "failing check", "failing checks"), "2 failing checks"},
		{"list(3 names)", list([]string{"a", "b", "c"}), "a, b, c"},
		{"list(4 names)", list([]string{"a", "b", "c", "d"}), "a, b, c and 1 more"},
		{"series(1 name)", series(strings.Fields("a")), "a"},
		{"series(2 names)", series(strings.Fields("a b")), "a and b"},
		{"series(3 names)", series(strings.Fields("a b c")), "a, b, and c"},
	}

	for _, tt := range tests {
		t.Run(tt.call, func(t *testing.T) {
			t.Parallel()

			if tt.got != tt.want {
				t.Errorf("%s = %q, want %q", tt.call, tt.got, tt.want)
			}
		})
	}
}
