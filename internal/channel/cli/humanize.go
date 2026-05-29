package cli

import (
	"fmt"
	"strings"
)

type unitStep struct {
	div  float64
	unit string
}

// humanizeTokens renders a token count compactly: <1000 as-is, thousands as
// "k", millions as "M", billions as "G". One decimal is kept only when the
// value is below 10 of its unit; a trailing ".0" is preserved for exactly
// round 1.0k/1.0M/1.0G to signal the unit boundary.
func humanizeTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return scale(float64(n), false, []unitStep{
		{1e9, "G"},
		{1e6, "M"},
		{1e3, "k"},
	})
}

// humanizeBytes renders a byte count as B/KB/MB/GB. Trailing ".0" is trimmed so
// round values render as "1KB"/"2GB".
func humanizeBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	return scale(float64(n), true, []unitStep{
		{1 << 30, "GB"},
		{1 << 20, "MB"},
		{1 << 10, "KB"},
	})
}

// scale formats v using the first unit whose divisor it meets. For magnitudes
// below 10 of the unit a single decimal is kept; trimZero strips a trailing
// ".0" (used for bytes, not tokens).
func scale(v float64, trimZero bool, units []unitStep) string {
	for _, u := range units {
		if v < u.div {
			continue
		}
		q := v / u.div
		if q < 10 {
			s := fmt.Sprintf("%.1f", q)
			if trimZero {
				s = strings.TrimSuffix(s, ".0")
			}
			return s + u.unit
		}
		return fmt.Sprintf("%.0f", q) + u.unit
	}
	// v smaller than the smallest unit divisor; fall back to integer.
	return strings.TrimSuffix(fmt.Sprintf("%.0f", v), ".0")
}
