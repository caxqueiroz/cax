package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/caxqueiroz/cax/internal/config"
)

// fakeSchedules is an in-memory scheduleBackend for testing /schedule CRUD.
type fakeSchedules struct {
	items   map[string]config.ScheduleConfig
	reloads int
	listErr error
}

func newFakeSchedules() *fakeSchedules {
	return &fakeSchedules{items: make(map[string]config.ScheduleConfig)}
}

func (f *fakeSchedules) List(_ context.Context) ([]config.ScheduleConfig, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]config.ScheduleConfig, 0, len(f.items))
	for _, sc := range f.items {
		out = append(out, sc)
	}
	return out, nil
}

func (f *fakeSchedules) Upsert(_ context.Context, sc config.ScheduleConfig) error {
	f.items[sc.Name] = sc
	return nil
}

func (f *fakeSchedules) Reload(_ context.Context) error {
	f.reloads++
	return nil
}

func TestScheduleNoBackend(t *testing.T) {
	m := newModel(80, 24)
	out := m.cmdSchedule("list")
	if !strings.Contains(out, "not available") {
		t.Errorf("expected unavailable message without backend, got %q", out)
	}
}

func TestScheduleListEmpty(t *testing.T) {
	m := newModel(80, 24)
	m.sched = newFakeSchedules()
	out := m.cmdSchedule("list")
	if !strings.Contains(out, "no schedules") {
		t.Errorf("expected empty message, got %q", out)
	}
}

func TestScheduleAddListReload(t *testing.T) {
	fk := newFakeSchedules()
	m := newModel(80, 24)
	m.sched = fk

	out := m.cmdSchedule(`add nightly "0 0 * * *" "daily report" cli`)
	if !strings.Contains(out, "nightly") || !strings.Contains(out, "added") {
		t.Errorf("add output = %q", out)
	}
	sc, ok := fk.items["nightly"]
	if !ok {
		t.Fatal("schedule not upserted")
	}
	if sc.Cron != "0 0 * * *" || sc.Prompt != "daily report" || sc.Channel != "cli" || !sc.Enabled {
		t.Errorf("upserted schedule wrong: %+v", sc)
	}
	if fk.reloads != 1 {
		t.Errorf("expected 1 reload after add, got %d", fk.reloads)
	}

	list := m.cmdSchedule("list")
	for _, want := range []string{"nightly", "0 0 * * *", "cli", "on"} {
		if !strings.Contains(list, want) {
			t.Errorf("list missing %q in:\n%s", want, list)
		}
	}
}

func TestScheduleAddDefaultsChannel(t *testing.T) {
	fk := newFakeSchedules()
	m := newModel(80, 24)
	m.sched = fk

	m.cmdSchedule(`add a "*/5 * * * *" "ping"`)
	sc := fk.items["a"]
	if sc.Channel != "cli" {
		t.Errorf("expected default channel cli, got %q", sc.Channel)
	}
}

func TestScheduleDisableEnableRemove(t *testing.T) {
	fk := newFakeSchedules()
	fk.items["a"] = config.ScheduleConfig{Name: "a", Cron: "0 0 * * *", Prompt: "p", Channel: "cli", Enabled: true}
	m := newModel(80, 24)
	m.sched = fk

	m.cmdSchedule("disable a")
	if fk.items["a"].Enabled {
		t.Error("disable should set Enabled=false")
	}
	if fk.reloads != 1 {
		t.Errorf("disable should reload once, got %d", fk.reloads)
	}

	m.cmdSchedule("enable a")
	if !fk.items["a"].Enabled {
		t.Error("enable should set Enabled=true")
	}

	out := m.cmdSchedule("remove a")
	if fk.items["a"].Enabled {
		t.Error("remove should soft-disable (Enabled=false)")
	}
	if !strings.Contains(out, "removed") && !strings.Contains(out, "disabled") {
		t.Errorf("remove output = %q", out)
	}
}

func TestScheduleEnableUnknown(t *testing.T) {
	fk := newFakeSchedules()
	m := newModel(80, 24)
	m.sched = fk
	out := m.cmdSchedule("enable ghost")
	if !strings.Contains(out, "no schedule") {
		t.Errorf("expected not-found message, got %q", out)
	}
	if fk.reloads != 0 {
		t.Errorf("no reload expected on unknown schedule, got %d", fk.reloads)
	}
}

func TestScheduleUsageAndUnknownSub(t *testing.T) {
	fk := newFakeSchedules()
	m := newModel(80, 24)
	m.sched = fk
	for _, in := range []string{"", "bogus"} {
		out := m.cmdSchedule(in)
		if !strings.Contains(out, "usage") {
			t.Errorf("cmdSchedule(%q) should show usage, got %q", in, out)
		}
	}
}

func TestScheduleAddBadArgs(t *testing.T) {
	fk := newFakeSchedules()
	m := newModel(80, 24)
	m.sched = fk
	out := m.cmdSchedule("add onlyname")
	if !strings.Contains(out, "usage") {
		t.Errorf("expected usage on bad add args, got %q", out)
	}
	if len(fk.items) != 0 {
		t.Errorf("nothing should be upserted on bad args")
	}
}
