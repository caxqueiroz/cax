package cli

import "testing"

func TestHumanizeTokens(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{7, "7"},
		{812, "812"},
		{999, "999"},
		{1000, "1.0k"},
		{1234, "1.2k"},
		{124000, "124k"},
		{812345, "812k"},
		{1500000, "1.5M"},
		{3200000, "3.2M"},
		{1000000000, "1.0G"},
	}
	for _, c := range cases {
		if got := humanizeTokens(c.in); got != c.want {
			t.Errorf("humanizeTokens(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHumanizeBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{1024, "1KB"},
		{1536, "1.5KB"},
		{18 * 1024 * 1024, "18MB"},
		{int64(2.0 * 1024 * 1024 * 1024), "2GB"},
	}
	for _, c := range cases {
		if got := humanizeBytes(c.in); got != c.want {
			t.Errorf("humanizeBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
