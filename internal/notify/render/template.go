// Package render turns a [domain.Notification] into the text every
// destination sends. [Notification] is the one place text is built: no
// destination builds a title, a body, or its own rendering of the agent's
// statement. A kind with a message limit applies [TruncateUTF16] to the
// rendered text.
//
// The package registers no notifier kind and imports no notifier package.
package render

import "github.com/sortie-ai/sortie/internal/domain"

// Text is one notification rendered for every destination.
type Text struct {
	Title string
	Body  string

	// AgentText is the agent's statement; empty when there is none.
	AgentText string

	// Operator is the operator rendering as one string.
	Operator string

	// Public is the public rendering; it never holds envelope data.
	Public string
}

// Notification renders n for every destination. It is the seam a
// configurable template will replace; today it delegates to the built-in
// rendering unchanged.
func Notification(n domain.Notification) Text {
	return builtin(n)
}
