package server

import (
	"fmt"
	"strconv"
	"time"
)

// FormatInt formats an int64 with comma thousand separators.
func FormatInt(v int64) string {
	// Format first to avoid negation overflow on math.MinInt64.
	s := strconv.FormatInt(v, 10)

	digits := s
	negative := false
	if s[0] == '-' {
		negative = true
		digits = s[1:]
	}

	n := len(digits)
	if n <= 3 {
		return s
	}

	commas := (n - 1) / 3
	buf := make([]byte, n+commas)
	j := len(buf) - 1
	for i := n - 1; i >= 0; i-- {
		buf[j] = digits[i]
		j--
		if (n-i)%3 == 0 && i > 0 {
			buf[j] = ','
			j--
		}
	}

	if negative {
		return "-" + string(buf)
	}
	return string(buf)
}

// FormatDuration formats a duration as a human-readable string with at
// most three components. When days are present, seconds are dropped.
// Negative durations return "0s".
func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}

	totalSec := int(d.Seconds())
	days := totalSec / 86400
	hours := (totalSec % 86400) / 3600
	minutes := (totalSec % 3600) / 60
	seconds := totalSec % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}
