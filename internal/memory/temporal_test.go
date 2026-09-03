package memory

import (
	"math"
	"testing"
	"time"
)

func mustTime(t *testing.T, date string) time.Time {
	t.Helper()
	tt, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatalf("parse %s: %v", date, err)
	}
	return tt.UTC()
}

func TestClassifyTemporalIntent_Keywords(t *testing.T) {
	cases := []struct {
		query string
		want  TemporalIntent
	}{
		{"", TemporalIntentNone},
		{"what is the host os", TemporalIntentNone},
		{"current host os", TemporalIntentCurrent},
		{"what is the OS now", TemporalIntentCurrent},
		{"yesterday we scanned 10.0.0.1", TemporalIntentPast},
		{"the previous host was Windows", TemporalIntentPast},
		{"last week we found CVE-2026-1234", TemporalIntentPast},
		{"tomorrow we patch the server", TemporalIntentFuture},
		{"planned maintenance next week", TemporalIntentFuture},
		{"will upgrade to Windows 11", TemporalIntentFuture},
		{"what happened on 2026-08-15", TemporalIntentAtDate},
	}
	for _, c := range cases {
		if got := ClassifyTemporalIntent(c.query); got != c.want {
			t.Errorf("Classify(%q) = %v, want %v", c.query, got, c.want)
		}
	}
}

func TestTemporalScore_SupersededAlwaysDemoted(t *testing.T) {
	now := mustTime(t, "2026-09-02")
	inst := &FactInstance{LifecycleState: LifecycleSuperseded, EventDate: "2026-09-02"}
	ts := TemporalScoreInstance(inst, TemporalIntentCurrent, "", now)
	if ts.Delta != -MaxTemporalDelta {
		t.Errorf("superseded delta = %v, want %v", ts.Delta, -MaxTemporalDelta)
	}
}

func TestTemporalScore_FutureEventHighActivation(t *testing.T) {
	now := mustTime(t, "2026-09-02")
	future := &FactInstance{
		LifecycleState: LifecycleActive,
		EventDate:      "2026-09-05", // 3 days out
	}
	ts := TemporalScoreInstance(future, TemporalIntentFuture, "", now)
	if ts.Delta <= 0 {
		t.Errorf("future event with future query must yield positive delta, got %v", ts.Delta)
	}
	// Close future → activation 1.0 → delta = MaxTemporalDelta
	if math.Abs(ts.Delta-MaxTemporalDelta) > 1e-9 {
		t.Errorf("close-future delta = %v, want %v", ts.Delta, MaxTemporalDelta)
	}
}

func TestTemporalScore_PastEventDecaysWithAge(t *testing.T) {
	now := mustTime(t, "2026-09-02")
	recent := &FactInstance{LifecycleState: LifecycleActive, EventDate: "2026-09-01"}   // 1 day past
	old := &FactInstance{LifecycleState: LifecycleActive, EventDate: "2026-08-01"}     // ~32 days past
	ancient := &FactInstance{LifecycleState: LifecycleActive, EventDate: "2025-01-01"} // ~610 days past

	tsRecent := TemporalScoreInstance(recent, TemporalIntentPast, "", now)
	tsOld := TemporalScoreInstance(old, TemporalIntentPast, "", now)
	tsAncient := TemporalScoreInstance(ancient, TemporalIntentPast, "", now)

	// Recent > old > ancient (monotonic decay).
	if !(tsRecent.Delta > tsOld.Delta && tsOld.Delta > tsAncient.Delta) {
		t.Errorf("past decay not monotonic: recent=%v old=%v ancient=%v", tsRecent.Delta, tsOld.Delta, tsAncient.Delta)
	}
	// Ancient still > 0 (bounded decay, never fully vanishes).
	if tsAncient.Delta <= 0 {
		t.Errorf("ancient past must still have positive nudge, got %v", tsAncient.Delta)
	}
}

func TestTemporalScore_MisalignedIntentNegative(t *testing.T) {
	now := mustTime(t, "2026-09-02")
	future := &FactInstance{LifecycleState: LifecycleActive, EventDate: "2026-09-10"} // future
	// Query wants past; instance is future → misaligned → negative.
	ts := TemporalScoreInstance(future, TemporalIntentPast, "", now)
	if ts.Delta >= 0 {
		t.Errorf("misaligned future/past must be negative, got %v", ts.Delta)
	}
}

func TestTemporalScore_NoneIntentNoNudge(t *testing.T) {
	now := mustTime(t, "2026-09-02")
	undated := &FactInstance{LifecycleState: LifecycleActive}
	ts := TemporalScoreInstance(undated, TemporalIntentNone, "", now)
	if ts.Delta != 0 {
		t.Errorf("undated + no-intent must be 0, got %v", ts.Delta)
	}
}

func TestTemporalScore_DeltaBounded(t *testing.T) {
	now := mustTime(t, "2026-09-02")
	inst := &FactInstance{LifecycleState: LifecycleActive, EventDate: "2026-09-02"}
	ts := TemporalScoreInstance(inst, TemporalIntentCurrent, "", now)
	if ts.Delta > MaxTemporalDelta || ts.Delta < -MaxTemporalDelta {
		t.Errorf("delta %v out of [-%v, +%v]", ts.Delta, MaxTemporalDelta, MaxTemporalDelta)
	}
}

func TestTemporalScore_AtDateMatchesInstance(t *testing.T) {
	now := mustTime(t, "2026-09-02")
	inst := &FactInstance{LifecycleState: LifecycleActive, EventDate: "2026-08-15"}
	// Query date matches instance event_date → full positive boost.
	ts := TemporalScoreInstance(inst, TemporalIntentAtDate, "2026-08-15", now)
	if ts.Delta != MaxTemporalDelta {
		t.Errorf("matching at-date delta = %v, want %v", ts.Delta, MaxTemporalDelta)
	}
}

func TestTemporalScore_AtDateMismatchPenalized(t *testing.T) {
	now := mustTime(t, "2026-09-02")
	inst := &FactInstance{LifecycleState: LifecycleActive, EventDate: "2026-08-15"}
	// Query date does NOT match instance event_date → negative nudge so the
	// wrong-date instance sinks rather than floats.
	ts := TemporalScoreInstance(inst, TemporalIntentAtDate, "2026-06-01", now)
	if ts.Delta >= 0 {
		t.Errorf("mismatched at-date must be negative, got %v", ts.Delta)
	}
}

func TestTemporalScore_AtDateUndatedInstanceSlightNegative(t *testing.T) {
	now := mustTime(t, "2026-09-02")
	undated := &FactInstance{LifecycleState: LifecycleActive}
	ts := TemporalScoreInstance(undated, TemporalIntentAtDate, "2026-08-15", now)
	if ts.Delta > 0 {
		t.Errorf("undated instance on at-date query must not be positive, got %v", ts.Delta)
	}
}

func TestApplyTemporalAdjustment_AdditiveAndPreservesOrder(t *testing.T) {
	now := mustTime(t, "2026-09-02")
	results := []FusionResult{
		{Instance: &FactInstance{ID: "past", LifecycleState: LifecycleActive, EventDate: "2026-09-01"}, CombinedScore: 0.5},
		{Instance: &FactInstance{ID: "future", LifecycleState: LifecycleActive, EventDate: "2026-09-05"}, CombinedScore: 0.5},
	}
	out := ApplyTemporalAdjustment(results, TemporalIntentFuture, "", now)
	// Future-aligned instance should be boosted above the past one.
	if out[0].Instance.ID != "future" {
		t.Errorf("future intent must rank future instance first, got %q", out[0].Instance.ID)
	}
	// Scores stay in [0,1].
	for _, r := range out {
		if r.CombinedScore < 0 || r.CombinedScore > 1 {
			t.Errorf("adjusted score %v out of [0,1]", r.CombinedScore)
		}
	}
}

func TestBackfillTemporalAttribs(t *testing.T) {
	// 2026-08-15 is a Saturday.
	a, ok := BackfillTemporalAttribs("2026-08-15")
	if !ok {
		t.Fatal("expected ok for valid date")
	}
	if a.Year != 2026 || a.Month != 8 || a.Quarter != 3 {
		t.Errorf("attribs = %+v", a)
	}
	if !a.IsWeekend {
		t.Errorf("2026-08-15 (Saturday) must be weekend")
	}
	if a.DayOfWeek != int(time.Saturday) {
		t.Errorf("DayOfWeek = %v, want %v", a.DayOfWeek, time.Saturday)
	}

	_, ok = BackfillTemporalAttribs("not-a-date")
	if ok {
		t.Error("invalid date must return ok=false")
	}
	_, ok = BackfillTemporalAttribs("")
	if ok {
		t.Error("empty date must return ok=false")
	}
}

func TestTemporalActivation_Bounds(t *testing.T) {
	// Future within 7 days → 1.0.
	if a := temporalActivation(3); a != 1.0 {
		t.Errorf("3-day future activation = %v, want 1.0", a)
	}
	// Far future decays but stays >= 0.5.
	if a := temporalActivation(365); a < 0.5 {
		t.Errorf("365-day future activation = %v, must be >= 0.5", a)
	}
	// Recent past → near 1.0.
	if a := temporalActivation(-1); a < 0.9 {
		t.Errorf("1-day past activation = %v, want near 1.0", a)
	}
	// Ancient past → approaches 0.1 but never below.
	if a := temporalActivation(-10000); a < 0.1 {
		t.Errorf("ancient past activation = %v, must be >= 0.1", a)
	}
	if a := temporalActivation(-10000); a > 0.11 {
		t.Errorf("ancient past activation = %v, must approach 0.1", a)
	}
}
