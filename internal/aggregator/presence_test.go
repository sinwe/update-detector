package aggregator

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"update-detector/internal/checker"
	"update-detector/internal/notifier"
)

// recordingNotifier captures every Event instead of delivering it.
type recordingNotifier struct {
	mu  sync.Mutex
	evs []notifier.Event
}

func (r *recordingNotifier) Name() string { return "test" }

func (r *recordingNotifier) Send(_ context.Context, ev notifier.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, ev)
	return nil
}

func (r *recordingNotifier) events() []notifier.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notifier.Event(nil), r.evs...)
}

// presenceFixture enrolls + approves id and -- unless noReport -- stores
// a report for it (so LastSeen is set, the watcher's "known host"
// gate). Returns the watcher (with a controllable clock), hub, and
// recorder. The watcher starts at t0 with 5m debounce.
func presenceFixture(t *testing.T, id, hostname string, noReport bool) (*PresenceWatcher, *CompanionHub, *recordingNotifier, *time.Time) {
	t.Helper()
	reg := NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	if _, _, err := reg.Enroll(id, hostname, "tok"); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetStatus(id, StatusApproved); err != nil {
		t.Fatal(err)
	}
	if !noReport {
		if _, err := reg.Report(id, "tok", checker.Status{Hostname: hostname, OK: true}); err != nil {
			t.Fatal(err)
		}
	}
	hub := NewCompanionHub()
	rec := &recordingNotifier{}
	now := time.Now()
	w := NewPresenceWatcher(reg, hub, notifier.NewManager(rec), 5*time.Minute)
	w.now = func() time.Time { return now }
	return w, hub, rec, &now
}

func TestPresenceAlertsAfterDebounce(t *testing.T) {
	w, _, rec, now := presenceFixture(t, "a1", "web01", false)
	ctx := context.Background()

	w.checkOnce(ctx)
	if got := rec.events(); len(got) != 0 {
		t.Fatalf("expected silence on first sighting, got %#v", got)
	}

	*now = now.Add(4 * time.Minute)
	w.checkOnce(ctx)
	if got := rec.events(); len(got) != 0 {
		t.Fatalf("expected silence before the debounce elapses, got %#v", got)
	}

	*now = now.Add(2 * time.Minute) // 6m total
	w.checkOnce(ctx)
	got := rec.events()
	if len(got) != 1 {
		t.Fatalf("expected 1 offline alert, got %#v", got)
	}
	if got[0].Hostname != "web01" || got[0].Title != "went offline" || !got[0].Brief {
		t.Fatalf("unexpected offline event: %#v", got[0])
	}

	// Still down: no repeat.
	*now = now.Add(10 * time.Minute)
	w.checkOnce(ctx)
	if got := rec.events(); len(got) != 1 {
		t.Fatalf("expected no repeat alert, got %#v", got)
	}
}

func TestPresenceRecovery(t *testing.T) {
	w, hub, rec, now := presenceFixture(t, "a1", "web01", false)
	ctx := context.Background()

	w.checkOnce(ctx)
	*now = now.Add(6 * time.Minute)
	w.checkOnce(ctx)
	if got := rec.events(); len(got) != 1 {
		t.Fatalf("expected offline alert first, got %#v", got)
	}

	res := hub.Connect("a1", KindCompanion, "v0.0.0-test")
	defer hub.Disconnect("a1", res.Ch)
	*now = now.Add(time.Minute)
	w.checkOnce(ctx)

	got := rec.events()
	if len(got) != 2 {
		t.Fatalf("expected offline + recovery, got %#v", got)
	}
	if got[1].Title != "is back online" || !got[1].Brief {
		t.Fatalf("unexpected recovery event: %#v", got[1])
	}

	// Still up: silence.
	w.checkOnce(ctx)
	if got := rec.events(); len(got) != 2 {
		t.Fatalf("expected silence while online, got %#v", got)
	}
}

func TestPresenceFlapStaysSilent(t *testing.T) {
	w, hub, rec, now := presenceFixture(t, "a1", "web01", false)
	ctx := context.Background()

	// Down 2m, then back before the 5m debounce: neither an alert
	// nor a recovery may fire.
	w.checkOnce(ctx)
	*now = now.Add(2 * time.Minute)
	w.checkOnce(ctx)
	res := hub.Connect("a1", KindAgent, "")
	defer hub.Disconnect("a1", res.Ch)
	w.checkOnce(ctx)

	if got := rec.events(); len(got) != 0 {
		t.Fatalf("expected complete silence for a sub-debounce flap, got %#v", got)
	}
}

func TestPresenceIgnoresNeverReportedHost(t *testing.T) {
	w, _, rec, now := presenceFixture(t, "a1", "web01", true)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		*now = now.Add(10 * time.Minute)
		w.checkOnce(ctx)
	}
	if got := rec.events(); len(got) != 0 {
		t.Fatalf("expected silence for a host that never reported, got %#v", got)
	}
}

func TestPresenceIgnoresRejectedHost(t *testing.T) {
	w, _, rec, now := presenceFixture(t, "a1", "web01", false)
	ctx := context.Background()

	if err := w.registry.SetStatus("a1", StatusRejected); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		*now = now.Add(10 * time.Minute)
		w.checkOnce(ctx)
	}
	if got := rec.events(); len(got) != 0 {
		t.Fatalf("expected silence for a rejected host, got %#v", got)
	}
}

func TestPresenceDisabledWhenDebounceNonPositive(t *testing.T) {
	w, _, rec, now := presenceFixture(t, "a1", "web01", false)
	w.offlineAfter = 0
	ctx := context.Background()

	*now = now.Add(time.Hour)
	w.checkOnce(ctx)
	if got := rec.events(); len(got) != 0 {
		t.Fatalf("expected silence when disabled, got %#v", got)
	}
}

func TestPresenceAlertRendersWithoutStatusTrailer(t *testing.T) {
	w, _, rec, now := presenceFixture(t, "a1", "web01", false)
	ctx := context.Background()

	w.checkOnce(ctx)
	*now = now.Add(6 * time.Minute)
	w.checkOnce(ctx)
	got := rec.events()
	if len(got) != 1 {
		t.Fatalf("expected 1 offline alert, got %#v", got)
	}
	msg := notifier.FormatMessage(got[0])
	if want := "<b>web01</b>: went offline"; !strings.Contains(msg, want) {
		t.Fatalf("expected %q in %q", want, msg)
	}
	if strings.Contains(msg, "Upgradable:") {
		t.Fatalf("brief presence alert must not carry the update trailer: %q", msg)
	}
}

func TestPresenceMutedHostStaysSilent(t *testing.T) {
	w, _, rec, now := presenceFixture(t, "a1", "web01", false)
	ctx := context.Background()

	if err := w.registry.SetNotifyDown("a1", false); err != nil {
		t.Fatal(err)
	}

	w.checkOnce(ctx)
	*now = now.Add(30 * time.Minute)
	w.checkOnce(ctx)
	if got := rec.events(); len(got) != 0 {
		t.Fatalf("expected silence for a muted host, got %#v", got)
	}

	// Unmuting a still-down host fires on the next round — the watcher
	// kept tracking offlineSince through the mute instead of restarting
	// the debounce.
	if err := w.registry.SetNotifyDown("a1", true); err != nil {
		t.Fatal(err)
	}
	w.checkOnce(ctx)
	got := rec.events()
	if len(got) != 1 || got[0].Title != "went offline" {
		t.Fatalf("expected 1 offline alert after unmuting, got %#v", got)
	}
}

func TestPresenceRecoverySuppressedWhenMuted(t *testing.T) {
	w, hub, rec, now := presenceFixture(t, "a1", "web01", false)
	ctx := context.Background()

	w.checkOnce(ctx)
	*now = now.Add(6 * time.Minute)
	w.checkOnce(ctx)
	if got := rec.events(); len(got) != 1 {
		t.Fatalf("expected offline alert first, got %#v", got)
	}

	// Muted after the offline alert: the recovery must stay silent too.
	if err := w.registry.SetNotifyDown("a1", false); err != nil {
		t.Fatal(err)
	}
	res := hub.Connect("a1", KindCompanion, "v0.0.0-test")
	defer hub.Disconnect("a1", res.Ch)
	*now = now.Add(time.Minute)
	w.checkOnce(ctx)
	if got := rec.events(); len(got) != 1 {
		t.Fatalf("expected no recovery for a muted host, got %#v", got)
	}
}

func TestPresenceTempMuteExpiresOnItsOwn(t *testing.T) {
	w, _, rec, now := presenceFixture(t, "a1", "web01", false)
	ctx := context.Background()

	// Snooze 10m from the fixture clock (not wall time — the watcher
	// evaluates MutedUntil against its own controllable now).
	until := now.Add(10 * time.Minute)
	if err := w.registry.SetMutedUntil("a1", &until); err != nil {
		t.Fatal(err)
	}

	w.checkOnce(ctx)
	*now = now.Add(6 * time.Minute) // past the 5m debounce, still muted
	w.checkOnce(ctx)
	if got := rec.events(); len(got) != 0 {
		t.Fatalf("expected silence while the mute holds, got %#v", got)
	}

	// 11m total: mute expired, host still down — the alert fires with
	// no explicit unmute, and no repeat after that.
	*now = now.Add(5 * time.Minute)
	w.checkOnce(ctx)
	got := rec.events()
	if len(got) != 1 || got[0].Title != "went offline" {
		t.Fatalf("expected 1 offline alert after the mute expired, got %#v", got)
	}
	*now = now.Add(10 * time.Minute)
	w.checkOnce(ctx)
	if got := rec.events(); len(got) != 1 {
		t.Fatalf("expected no repeat alert, got %#v", got)
	}
}
