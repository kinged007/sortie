package config

import "log/slog"

// Advisory is a non-fatal configuration observation recorded while a
// [ServiceConfig] is built or loaded. Check and Text are empty exactly
// for an advisory sourced from the worker extension section, which
// sortie validate never reports. Two advisories are the same advisory
// when their Message is equal and their Attrs are equal key by key and
// value by value (via [slog.Value.Equal]), in declared order; Check
// and Text take no part in that comparison.
type Advisory struct {
	Check   string
	Text    string
	Message string
	Attrs   []slog.Attr
}

// Advisories returns a new slice holding the advisories recorded on c,
// in recording order, so that a caller appending to the result never
// mutates c. A configuration that recorded none returns an empty,
// non-nil slice.
func (c ServiceConfig) Advisories() []Advisory {
	out := make([]Advisory, len(c.advisories))
	copy(out, c.advisories)
	return out
}

// AddAdvisories appends a to c's recorded advisories, in argument
// order. Callers add advisories only while c is being built, before it
// is returned or promoted.
func (c *ServiceConfig) AddAdvisories(a ...Advisory) {
	c.advisories = append(c.advisories, a...)
}
