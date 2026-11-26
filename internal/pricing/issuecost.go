package pricing

import (
	"fmt"
	"math"
	"strconv"
)

// microDollars is the resolution of a ceiling comparison: sums of recorded
// costs carry float error far below a millionth of a dollar, so rounding
// both sides to it makes a sum that equals the ceiling reach it.
const microDollars = 1e6

// RecordedCost is the recorded cost of an issue's finished runs, summed
// from their run_history rows.
type RecordedCost struct {
	// USD is the recorded cost summed over runs recorded estimated or partial.
	USD float64

	// Priced counts runs that spent tokens and carry a recorded cost.
	Priced int

	// Partial counts runs recorded partial.
	Partial int

	// Unpriced counts runs that spent tokens and carry no recorded cost.
	Unpriced int
}

// SessionCost is a running session's live estimate. The zero value is no
// session.
type SessionCost struct {
	USD    *float64
	Status CostStatus

	// Spent is true when the session spent tokens in some class.
	Spent bool
}

// IssueCost folds an issue's recorded cost and its running session's
// estimate into the issue's estimated cost. usd is the priced sum, a lower
// bound unless status is [CostEstimated]. A session that spent nothing
// prices nothing, so it neither makes an unpriced issue partial nor a fully
// priced one unpriced. IssueCost is pure.
func IssueCost(recorded RecordedCost, session SessionCost, spendComplete bool) (usd float64, status CostStatus) {
	total := recorded.USD
	priced := recorded.Priced > 0
	partial := recorded.Partial > 0
	unpriced := recorded.Unpriced > 0

	switch session.Status {
	case CostEstimated, CostPartial:
		if session.USD != nil {
			total += *session.USD
		}
		priced = priced || session.Spent
		partial = partial || session.Status == CostPartial
	case CostUnpriced:
		unpriced = unpriced || session.Spent
	}

	switch {
	case spendComplete && !unpriced && !partial:
		return total, CostEstimated
	case priced:
		return total, CostPartial
	case unpriced:
		return total, CostUnpriced
	default:
		return total, CostUnmeasured
	}
}

// CeilingReached reports whether usd has reached ceiling, both rounded to
// whole micro-dollars.
func CeilingReached(usd, ceiling float64) bool {
	return math.Round(usd*microDollars) >= math.Round(ceiling*microDollars)
}

// FormatUSD renders "$", whole dollars with comma thousands separators, and
// two decimals rounded to the cent. Rounding runs on integer cents so a
// value near a boundary does not split (999.999 renders "$1,000.00").
func FormatUSD(v float64) string {
	cents := int64(math.Round(v * 100))
	dollars := cents / 100
	remainder := cents % 100
	return fmt.Sprintf("$%s.%02d", groupThousands(dollars), remainder)
}

// groupThousands renders v with a comma between each group of three digits.
func groupThousands(v int64) string {
	digits := strconv.FormatInt(v, 10)
	sign := ""
	if v < 0 {
		sign, digits = "-", digits[1:]
	}
	var grouped []byte
	for i := range len(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped = append(grouped, ',')
		}
		grouped = append(grouped, digits[i])
	}
	return sign + string(grouped)
}
