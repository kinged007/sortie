package sentence

import (
	"math"
	"strconv"
	"strings"
	"text/template"
)

// funcs is the FuncMap every template in the table is parsed with.
var funcs = template.FuncMap{
	"duration": duration,
	"due":      due,
	"tokens":   tokens,
	"count":    count,
	"plural":   plural,
	"list":     list,
	"series":   series,
	"retry":    retry,
}

// duration words a span of seconds in at most two units. NaN and infinite
// spans read as unknown.
func duration(seconds float64) string {
	switch {
	case math.IsNaN(seconds) || math.IsInf(seconds, 0):
		return "unknown"
	case seconds < 1:
		return "under 1s"
	case seconds < 10:
		return strings.TrimSuffix(strconv.FormatFloat(math.Round(seconds*10)/10, 'f', 1, 64), ".0") + "s"
	case seconds < 59.5:
		return strconv.FormatFloat(math.Round(seconds), 'f', 0, 64) + "s"
	}

	minutes := int(math.Round(seconds / 60))
	if minutes < 60 {
		return strconv.Itoa(minutes) + "m"
	}
	hours := minutes / 60
	if minutes < 24*60 {
		return unitPair(hours, "h", minutes%60, "m")
	}
	return unitPair(hours/24, "d", hours%24, "h")
}

// unitPair joins a major and a minor unit, dropping a minor part of zero.
func unitPair(major int, majorUnit string, minor int, minorUnit string) string {
	text := strconv.Itoa(major) + majorUnit
	if minor == 0 {
		return text
	}
	return text + " " + strconv.Itoa(minor) + minorUnit
}

// due words a moment seconds ahead; a moment at hand or past reads as due now.
func due(seconds float64) string {
	if seconds < 1 {
		return "due now"
	}
	return "in " + duration(seconds)
}

// tokens words a token figure; nil is a figure no measurement produced.
func tokens(n *int64) string {
	switch {
	case n == nil:
		return "not measured"
	case *n >= 999_500:
		return millions(*n)
	case *n >= 1_000:
		return strconv.FormatInt((*n+500)/1_000, 10) + "K"
	}
	return strconv.FormatInt(*n, 10)
}

// millions words n at two decimals of millions with trailing zeros dropped.
// Integer arithmetic keeps a half-way figure from rounding down on float
// representation error.
func millions(n int64) string {
	hundredths := (n + 5_000) / 10_000
	whole, frac := hundredths/100, hundredths%100
	text := strconv.FormatInt(whole, 10)
	switch {
	case frac == 0:
	case frac%10 == 0:
		text += "." + strconv.FormatInt(frac/10, 10)
	default:
		text += "." + strconv.FormatInt(frac/10, 10) + strconv.FormatInt(frac%10, 10)
	}
	return text + "M"
}

// count writes n with comma-separated thousands.
func count(n int) string {
	digits := strconv.Itoa(n)
	sign := ""
	if n < 0 {
		sign, digits = "-", digits[1:]
	}
	var b strings.Builder
	b.WriteString(sign)
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// plural writes n and the noun form n calls for.
func plural(n int, one, many string) string {
	if n == 1 {
		return count(n) + " " + one
	}
	return count(n) + " " + many
}

// list joins the first three names and counts the rest.
func list(names []string) string {
	const shown = 3
	if len(names) <= shown {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:shown], ", ") + " and " + count(len(names)-shown) + " more"
}

// series joins names as prose: one as is, two with "and", more with commas and
// a final ", and".
func series(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	}
	return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
}

// retry words a queued retry as what comes due: the run a reaction or
// continuation retry starts, else its attempt number. runInline is the
// inline phrase of the run a reaction retry with no trigger starts.
func retry(trigger, runInline string, attempt int) string {
	switch {
	case trigger == "continuation":
		return "the next run"
	case trigger == "ci_fix":
		return "the CI fix run"
	case runInline != "":
		return "the " + runInline
	}
	return "retry " + count(attempt)
}
