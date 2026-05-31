package memory

import (
	"testing"
	"time"

	"github.com/caxqueiroz/cax/internal/config"
)

func TestUpsertAndListSchedules(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := t.Context()

	sc := config.ScheduleConfig{Name: "daily", Cron: "0 9 * * *", Prompt: "good morning", Channel: "cli", Enabled: true}
	if err := st.UpsertSchedule(ctx, sc); err != nil {
		t.Fatalf("UpsertSchedule: %v", err)
	}
	list, err := st.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d, want 1", len(list))
	}
	got := list[0]
	if got.Name != "daily" || got.Cron != "0 9 * * *" || got.Prompt != "good morning" || got.Channel != "cli" || !got.Enabled {
		t.Fatalf("schedule = %+v", got)
	}

	// Upsert same name updates in place (no duplicate).
	sc.Prompt = "rise and shine"
	sc.Enabled = false
	if err := st.UpsertSchedule(ctx, sc); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	list, err = st.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("after update len = %d, want 1 (upsert, not insert)", len(list))
	}
	if list[0].Prompt != "rise and shine" || list[0].Enabled {
		t.Fatalf("updated schedule = %+v", list[0])
	}
}

func TestSetLastRun(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := t.Context()
	if err := st.UpsertSchedule(ctx, config.ScheduleConfig{Name: "job", Cron: "* * * * *", Prompt: "p", Channel: "cli", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	when := time.Now().UTC().Truncate(time.Second)
	if err := st.SetLastRun(ctx, "job", when); err != nil {
		t.Fatalf("SetLastRun: %v", err)
	}
	var stored time.Time
	if err := st.db.QueryRow(`SELECT last_run FROM schedules WHERE name='job'`).Scan(&stored); err != nil {
		t.Fatalf("read last_run: %v", err)
	}
	if !stored.UTC().Equal(when) {
		t.Fatalf("last_run = %v, want %v", stored.UTC(), when)
	}
}
