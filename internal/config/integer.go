package config

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// ErrIntegerOutOfRange reports that a numeric value lies outside the
// range the platform int type can hold. Callers detect it with
// [errors.Is].
var ErrIntegerOutOfRange = fmt.Errorf(
	"value is outside the range an integer setting accepts, %d to %d", math.MinInt, math.MaxInt,
)

// IntFromNumber converts v to an int, reporting false when v is not a
// numeric type or its value falls outside the range the platform int
// can hold. No arm of the conversion truncates or wraps an
// out-of-range value: the range test always runs before v is cast to
// int.
func IntFromNumber(v any) (int, bool) {
	return intFromNumber(v, strconv.IntSize)
}

// intFromNumber is [IntFromNumber] parameterized by the target width in
// bits, which must be 32 or 64. Production code always calls it through
// [IntFromNumber] with the platform's own width; the parameter exists so
// tests can exercise the 32-bit range boundary on a 64-bit host.
func intFromNumber(v any, bits int) (int, bool) {
	lo := int64(-1) << uint(bits-1)      //nolint:gosec // G115: bits is always 32 or 64, so bits-1 is always non-negative
	hi := (int64(1) << uint(bits-1)) - 1 //nolint:gosec // G115: bits is always 32 or 64, so bits-1 is always non-negative

	switch n := v.(type) {
	case int:
		n64 := int64(n)
		if n64 < lo || n64 > hi {
			return 0, false
		}
		return n, true
	case int64:
		if n < lo || n > hi {
			return 0, false
		}
		return int(n), true
	case uint64:
		if n > uint64(hi) { //nolint:gosec // G115: hi is 2^(bits-1)-1, always non-negative
			return 0, false
		}
		return int(n), true //nolint:gosec // G115: n was just bounds-checked against hi above
	case float64:
		if n != math.Trunc(n) {
			return 0, false
		}
		// The upper bound is exclusive: float64(hi) rounds up to
		// 2^(bits-1), one past hi, so a numeral that rounds to exactly
		// that power of two must still be rejected.
		upperExclusive := math.Ldexp(1, bits-1)
		if float64(lo) <= n && n < upperExclusive {
			return int(n), true
		}
		return 0, false
	default:
		return 0, false
	}
}

// integerFaultMessage returns [ErrIntegerOutOfRange]'s own text when err
// wraps that sentinel, and fallback otherwise.
func integerFaultMessage(err error, fallback string) string {
	if errors.Is(err, ErrIntegerOutOfRange) {
		return ErrIntegerOutOfRange.Error()
	}
	return fallback
}
