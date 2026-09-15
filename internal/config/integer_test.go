package config

import (
	"math"
	"testing"
)

func TestIntFromNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		v    any
		bits int
		want int
		ok   bool
	}{
		{"int64 min at 64 bits converts", int64(math.MinInt64), 64, math.MinInt64, true},
		{"int64 max at 64 bits converts", int64(math.MaxInt64), 64, math.MaxInt64, true},
		{"uint64 at hi converts", uint64(math.MaxInt64), 64, math.MaxInt64, true},
		{"uint64 one past hi is rejected", uint64(math.MaxInt64) + 1, 64, 0, false},
		{"float64 lower bound converts", -9223372036854775808.0, 64, math.MinInt64, true},
		{"float64 nearest representable below 2^63 converts", 9223372036854774784.0, 64, 9223372036854774784, true},
		{"float64 exactly 2^63 is rejected", 9223372036854775808.0, 64, 0, false},
		{"float64 just below MinInt64 is rejected", -9223372036854777856.0, 64, 0, false},
		{"float64 +Inf is rejected", math.Inf(1), 64, 0, false},
		{"float64 -Inf is rejected", math.Inf(-1), 64, 0, false},
		{"float64 NaN is rejected", math.NaN(), 64, 0, false},
		{"float64 fractional is rejected", 1.5, 64, 0, false},
		{"unsupported type string is rejected", "42", 64, 0, false},
		{"unsupported type bool is rejected", true, 64, 0, false},

		{"int at 32-bit max converts", 2147483647, 32, 2147483647, true},
		{"int at 32-bit min converts", -2147483648, 32, -2147483648, true},
		{"int64 one past 32-bit max is rejected", int64(2147483648), 32, 0, false},
		{"int64 one past 32-bit min is rejected", int64(-2147483649), 32, 0, false},
		{"uint64 at 32-bit max converts", uint64(2147483647), 32, 2147483647, true},
		{"uint64 one past 32-bit max is rejected", uint64(2147483648), 32, 0, false},
		{"float64 at 32-bit max converts", float64(2147483647), 32, 2147483647, true},
		{"float64 at 32-bit min converts", float64(-2147483648), 32, -2147483648, true},
		{"float64 one past 32-bit max is rejected", float64(2147483648), 32, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := intFromNumber(tt.v, tt.bits)

			if ok != tt.ok {
				t.Fatalf("intFromNumber(%v, %d) ok = %v, want %v", tt.v, tt.bits, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("intFromNumber(%v, %d) = %d, want %d", tt.v, tt.bits, got, tt.want)
			}
		})
	}
}

// TestIntFromNumber_PlatformWidth confirms IntFromNumber delegates to
// intFromNumber at strconv.IntSize rather than a fixed width, so a
// 64-bit host rejects a value only a wider type could hold.
func TestIntFromNumber_PlatformWidth(t *testing.T) {
	t.Parallel()

	got, ok := IntFromNumber(int64(42))
	if !ok || got != 42 {
		t.Errorf("IntFromNumber(int64(42)) = (%d, %v), want (42, true)", got, ok)
	}

	if _, ok := IntFromNumber(uint64(math.MaxUint64)); ok {
		t.Errorf("IntFromNumber(uint64(MaxUint64)) ok = true, want false")
	}
}

func TestErrIntegerOutOfRange_Text(t *testing.T) {
	t.Parallel()

	want := "value is outside the range an integer setting accepts, -9223372036854775808 to 9223372036854775807"
	if got := ErrIntegerOutOfRange.Error(); got != want {
		t.Errorf("ErrIntegerOutOfRange.Error() = %q, want %q", got, want)
	}
}

func TestIntegerFaultMessage(t *testing.T) {
	t.Parallel()

	if got := integerFaultMessage(ErrIntegerOutOfRange, "fallback"); got != ErrIntegerOutOfRange.Error() {
		t.Errorf("integerFaultMessage(ErrIntegerOutOfRange, ...) = %q, want %q", got, ErrIntegerOutOfRange.Error())
	}
	if got := integerFaultMessage(nil, "fallback"); got != "fallback" {
		t.Errorf("integerFaultMessage(nil, %q) = %q, want %q", "fallback", got, "fallback")
	}
}
