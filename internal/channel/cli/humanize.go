package cli

import (
	"fmt"
	"strings"
	"time"
)

// humanizeDuration formats a wall-clock duration tersely:
//
//	< 1s   -> "0.3s"
//	< 10s  -> "3.2s"  (one decimal)
//	< 60s  -> "32s"
//	< 60m  -> "12m"  (or "12m 34s" if precise)
//	< 24h  -> "1h 12m"
//	>=24h  -> "1d 2h"
//
// `precise` toggles whether sub-units are shown alongside the dominant unit.
// Live timers prefer false (less jitter); the per-turn trailer prefers true.
func humanizeDuration(d time.Duration, precise bool) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < 10*time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d / time.Minute)
		if precise {
			s := int((d % time.Minute) / time.Second)
			if s > 0 {
				return fmt.Sprintf("%dm %ds", m, s)
			}
		}
		return fmt.Sprintf("%dm", m)
	}
	if d < 24*time.Hour {
		h := int(d / time.Hour)
		m := int((d % time.Hour) / time.Minute)
		if m > 0 {
			return fmt.Sprintf("%dh %dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
	days := int(d / (24 * time.Hour))
	h := int((d % (24 * time.Hour)) / time.Hour)
	if h > 0 {
		return fmt.Sprintf("%dd %dh", days, h)
	}
	return fmt.Sprintf("%dd", days)
}

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
