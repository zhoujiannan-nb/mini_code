package super


import (
	"path/filepath"
	"testing"
	"time"

	"github.com/user/mini_code/util"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := util.NewDatabase(filepath.Join(t.TempDir(), "super_test.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db)
}

func TestScheduleRoundtrip(t *testing.T) {
	s := newTestStore(t)
	sc := &Schedule{ID: "sch-aaaaaa", Name: "打卡", Text: "该打卡了", Session: "s-1", EverySec: 3600, Enabled: true}
	if err := s.CreateSchedule(sc); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetSchedule("sch-aaaaaa")
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.Text != "该打卡了" || got.EverySec != 3600 || !got.Enabled {
		t.Fatalf("field mismatch: %+v", got)
	}
	if err := s.ScheduleSetEnabled("sch-aaaaaa", false); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	got, _ = s.GetSchedule("sch-aaaaaa")
	if got.Enabled {
		t.Fatal("toggle off failed")
	}
	if err := s.ScheduleSetResult("sch-aaaaaa", time.Now().Format(time.RFC3339)); err != nil {
		t.Fatalf("result: %v", err)
	}
	got, _ = s.GetSchedule("sch-aaaaaa")
	if got.LastRun == "" {
		t.Fatal("last_run not recorded")
	}
	if err := s.DeleteSchedule("sch-aaaaaa"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, _ := s.ListSchedules()
	if len(list) != 0 {
		t.Fatalf("delete left %d rows", len(list))
	}
}

func TestScheduleDue(t *testing.T) {
	now := time.Now()
	off := &Schedule{Enabled: false, EverySec: 60}
	if off.Due(now) {
		t.Fatal("disabled schedule must not be due")
	}
	fresh := &Schedule{Enabled: true, EverySec: 3600}
	if !fresh.Due(now) {
		t.Fatal("never-run schedule should be due")
	}
	recent := &Schedule{Enabled: true, EverySec: 3600, LastRun: now.Add(-time.Minute).Format(time.RFC3339)}
	if recent.Due(now) {
		t.Fatal("recently run schedule should not be due")
	}
	stale := &Schedule{Enabled: true, EverySec: 60, LastRun: now.Add(-5 * time.Minute).Format(time.RFC3339)}
	if !stale.Due(now) {
		t.Fatal("stale schedule should be due")
	}
}

func TestLegacyTablesDropped(t *testing.T) {
	db, err := util.NewDatabase(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	defer db.Close()
	db.InitTable(`CREATE TABLE IF NOT EXISTS super_ticket (id TEXT PRIMARY KEY)`)
	db.InitTable(`CREATE TABLE IF NOT EXISTS super_source (id TEXT PRIMARY KEY)`)
	db.InitTable(`CREATE TABLE IF NOT EXISTS super_schedule (file TEXT PRIMARY KEY, note TEXT)`)
	_ = NewStore(db)
	row, err := db.FetchOne("SELECT name FROM sqlite_master WHERE type='table' AND name IN ('super_ticket','super_source') LIMIT 1")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if row != nil {
		t.Fatalf("legacy table survived: %v", row)
	}
	if _, err := db.Execute("INSERT INTO super_schedule(id,name,text,session,every_sec,enabled,last_run,created_at,updated_at) VALUES('x','n','t','',60,1,'','','')"); err != nil {
		t.Fatalf("new schema not in place: %v", err)
	}
}

func TestStateAndAudit(t *testing.T) {
	s := newTestStore(t)
	if err := s.StateSet("k", "v1"); err != nil {
		t.Fatal(err)
	}
	_ = s.StateSet("k", "v2")
	if s.StateGet("k") != "v2" {
		t.Fatal("StateSet upsert failed")
	}
	if err := s.AuditInsert("human", "test.act", "tgt", "d", "r"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.AuditRecent(10)
	if err != nil || len(rows) != 1 || rows[0].Action != "test.act" {
		t.Fatalf("audit: %v %v", rows, err)
	}
}

func TestParseEvery(t *testing.T) {
	for in, want := range map[string]int{"": 3600, "90": 90, "90m": 5400, "2h30m": 9000, "1d": 86400, "10s": 30} {
		got, err := parseEvery(in)
		if err != nil || got != want {
			t.Fatalf("parseEvery(%q)=%d,%v want %d", in, got, err, want)
		}
	}
	if _, err := parseEvery("3x"); err == nil {
		t.Fatal("expected error for 3x")
	}
}
