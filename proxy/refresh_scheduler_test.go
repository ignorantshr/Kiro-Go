package proxy

import (
	"testing"
	"time"

	"kiro-go/pool"
)

// computeRefreshInterval edge cases. All "cannot estimate" branches must fall
// back to refreshMaxInterval (the pre-change fixed cadence).
func TestComputeRefreshIntervalFallbacks(t *testing.T) {
	const min = refreshMinInterval
	const max = refreshMaxInterval

	cases := []struct {
		name        string
		prevCurrent float64
		current     float64
		limit       float64
		elapsed     time.Duration
		hasPrev     bool
		want        time.Duration
	}{
		{"no limit", 0, 0, 0, time.Minute, true, max},
		{"no prev snapshot", 0, 50, 100, time.Minute, false, max},
		{"zero elapsed", 10, 20, 100, 0, true, max},
		{"negative elapsed", 10, 20, 100, -time.Minute, true, max},
		{"idle (no growth)", 50, 50, 100, time.Minute, true, max},
		{"usage decreased (reset)", 80, 10, 100, time.Minute, true, max},
		{"already past 95pct threshold", 90, 96, 100, time.Minute, true, max},
		{"already at 95pct exactly", 90, 95, 100, time.Minute, true, max},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeRefreshInterval(tc.prevCurrent, tc.current, tc.limit, tc.elapsed, tc.hasPrev)
			if got != tc.want {
				t.Fatalf("computeRefreshInterval = %v, want %v", got, tc.want)
			}
		})
	}
	_ = min
}

// A fast-burning account near the threshold must be clamped to the minimum.
func TestComputeRefreshIntervalClampsToMin(t *testing.T) {
	// threshold = 95. current=90, remaining=5. Burned 40 in 60s → ~0.667/s.
	// eta = 5 / 0.667 ≈ 7.5s; *0.5 ≈ 3.75s → below min, clamp to min.
	got := computeRefreshInterval(50, 90, 100, time.Minute, true)
	if got != refreshMinInterval {
		t.Fatalf("expected clamp to min %v, got %v", refreshMinInterval, got)
	}
}

// A slow-burning account far from the threshold yields an interval between
// min and max (proportional to ETA).
func TestComputeRefreshIntervalProportional(t *testing.T) {
	// threshold = 95. current=10, remaining=85. Burned 1 in 60s → 1/60 per s.
	// eta = 85 / (1/60) = 5100s; *0.5 = 2550s = 42.5min → clamp to max.
	got := computeRefreshInterval(9, 10, 100, time.Minute, true)
	if got != refreshMaxInterval {
		t.Fatalf("expected clamp to max for slow burn, got %v", got)
	}

	// Mid-range: threshold=95, current=80, remaining=15. Burned 5 in 60s → 1/12 per s.
	// eta = 15 / (1/12) = 180s; *0.5 = 90s. Between min(60s) and max → ~90s.
	// Float division (15 / (5/60.0) * 0.5) lands a few ns shy of an exact 90s,
	// so compare within a tolerance rather than for exact equality.
	got = computeRefreshInterval(75, 80, 100, time.Minute, true)
	want := 90 * time.Second
	if diff := got - want; diff < -time.Millisecond || diff > time.Millisecond {
		t.Fatalf("expected ~%v for mid-range burn, got %v", want, got)
	}
}

// Sanity: the scheduler reuses the same 95% ratio the pool blocks on.
func TestRefreshUsesPoolQuotaRatio(t *testing.T) {
	if pool.QuotaBlockRatio != 0.95 {
		t.Fatalf("test assumes QuotaBlockRatio=0.95, got %v", pool.QuotaBlockRatio)
	}
}

func TestSchedulerDueForUnknownAccount(t *testing.T) {
	s := newRefreshScheduler()
	if !s.Due("new", time.Now()) {
		t.Fatal("an account with no recorded state must be due")
	}
}

func TestSchedulerRecordSetsNextDue(t *testing.T) {
	s := newRefreshScheduler()
	now := time.Unix(1_000_000, 0)

	// First Record has no prior snapshot → max interval.
	s.Record("a", 50, 100, now)
	if s.Due("a", now) {
		t.Fatal("just-recorded account must not be immediately due")
	}
	if s.Due("a", now.Add(refreshMaxInterval-time.Second)) {
		t.Fatal("should not be due just before max interval elapses")
	}
	if !s.Due("a", now.Add(refreshMaxInterval+time.Second)) {
		t.Fatal("should be due after max interval elapses")
	}
}

func TestSchedulerRecordSecondSampleAdaptsCadence(t *testing.T) {
	s := newRefreshScheduler()
	t0 := time.Unix(1_000_000, 0)
	s.Record("a", 73, 100, t0) // seed snapshot, nextDue = t0 + max

	// Second sample 60s later: burned 2 (73→75). threshold=95, remaining=20,
	// burnRate=2/60 → eta=600s, *0.5=300s. Between min(60s) and max(30min) → ~300s.
	t1 := t0.Add(time.Minute)
	s.Record("a", 75, 100, t1)

	if s.Due("a", t1.Add(299*time.Second)) {
		t.Fatal("should not be due before adaptive ~300s interval")
	}
	if !s.Due("a", t1.Add(301*time.Second)) {
		t.Fatal("should be due after adaptive ~300s interval")
	}
}

func TestSchedulerBackoffDefersWithoutSnapshot(t *testing.T) {
	s := newRefreshScheduler()
	now := time.Unix(1_000_000, 0)

	s.Backoff("bad", now)
	if s.Due("bad", now.Add(refreshMaxInterval-time.Second)) {
		t.Fatal("backed-off account must not be due before max interval")
	}
	if !s.Due("bad", now.Add(refreshMaxInterval+time.Second)) {
		t.Fatal("backed-off account must be due after max interval")
	}
}

func TestSchedulerRetainEvictsStaleAccounts(t *testing.T) {
	s := newRefreshScheduler()
	now := time.Unix(1_000_000, 0)
	s.Record("keep", 10, 100, now)
	s.Record("drop", 10, 100, now)

	s.Retain(map[string]bool{"keep": true})

	// "drop" was evicted → treated as unknown → due again.
	if !s.Due("drop", now) {
		t.Fatal("evicted account should be treated as unknown (due)")
	}
	// "keep" retains its nextDue → not due yet.
	if s.Due("keep", now) {
		t.Fatal("retained account should keep its schedule")
	}
}
