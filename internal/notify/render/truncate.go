package render

import "strings"

// TruncateUTF16 returns the longest prefix of s that holds at most limit
// UTF-16 code units, cut at a rune boundary. Invalid UTF-8 is replaced with
// U+FFFD first, and no marker is appended.
func TruncateUTF16(s string, limit int) string {
	s = strings.ToValidUTF8(s, "�")
	units := 0
	for i, r := range s {
		cost := 1
		if r > 0xFFFF {
			cost = 2
		}
		if units+cost > limit {
			return s[:i]
		}
		units += cost
	}
	return s
}
