package memory

import (
	"testing"
	"time"
)

func TestRecordUsageAndRollups(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := t.Context()
	now := time.Now().UTC()

	// Insert rows at controlled timestamps directly to test the windows precisely.
	insert := func(ts time.Time, in, out int) {
		t.Helper()
		if _, err := st.db.ExecContext(ctx,
			`INSERT INTO usage(ts, provider, model, input_tokens, output_tokens, kind) VALUES (?, 'openai', 'm', ?, ?, 'chat')`,
			ts, in, out); err != nil {
			t.Fatalf("insert usage: %v", err)
		}
	}
	insert(now.Add(-1*time.Hour), 10, 1)     // within day, week, month
	insert(now.Add(-3*24*time.Hour), 20, 2)  // within week, month
	insert(now.Add(-10*24*time.Hour), 40, 4) // within month only
	insert(now.Add(-40*24*time.Hour), 80, 8) // outside all windows

	got, err := st.UsageRollups(ctx)
	if err != nil {
		t.Fatalf("UsageRollups: %v", err)
	}
	if got.Day.InputTokens != 10 || got.Day.OutputTokens != 1 {
		t.Fatalf("Day = %+v, want {10,1}", got.Day)
	}
	if got.Week.InputTokens != 30 || got.Week.OutputTokens != 3 {
		t.Fatalf("Week = %+v, want {30,3}", got.Week)
	}
	if got.Month.InputTokens != 70 || got.Month.OutputTokens != 7 {
		t.Fatalf("Month = %+v, want {70,7}", got.Month)
	}
}

func TestRecordUsageWritesRow(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := t.Context()
	if err := st.RecordUsage(ctx, "openai", "gpt", 5, 6, UsageChat); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	got, err := st.UsageRollups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Day.InputTokens != 5 || got.Day.OutputTokens != 6 {
		t.Fatalf("Day = %+v, want {5,6}", got.Day)
	}
}
