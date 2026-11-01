package aggregator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"update-detector/internal/checker"
	"update-detector/internal/notifier"
)

// fakeNotifier records what would have gone to Telegram. sent is closed on
// the first delivery so the async per-host path can be awaited without
// sleeping.
type fakeNotifier struct {
	mu     sync.Mutex
	events []notifier.Event
	err    error
	sent   chan struct{}
}

func newFakeNotifier() *fakeNotifier {
	return &fakeNotifier{sent: make(chan struct{}, 4)}
}

func (f *fakeNotifier) Name() string { return "fake" }

func (f *fakeNotifier) Send(_ context.Context, ev notifier.Event) error {
	f.mu.Lock()
	f.events = append(f.events, ev)
	f.mu.Unlock()
	select {
	case f.sent <- struct{}{}:
	default:
	}
	return f.err
}

func (f *fakeNotifier) snapshot() []notifier.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]notifier.Event(nil), f.events...)
}

// waitForEvent blocks until at least one Send has happened (or the test
// times out), returning the first event.
func (f *fakeNotifier) waitForEvent(t *testing.T) notifier.Event {
	t.Helper()
	select {
	case <-f.sent:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a notification")
	}
	evs := f.snapshot()
	if len(evs) == 0 {
		t.Fatal("expected at least one event")
	}
	return evs[0]
}

func TestNotifyStoreRoundTripAndMasking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify.json")
	s := NewNotifyStore(path)
	if err := s.Load(); err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if _, ok := s.Get("a1"); ok {
		t.Fatal("expected no config for an unknown host")
	}

	cfg := HostNotifyConfig{BotToken: "8891668888:AAH4xQsecretw2lE", ChatID: "-1001234567890", Enabled: true}
	if err := s.Set("a1", cfg); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !cfg.Configured() || !cfg.Active() {
		t.Fatal("expected a complete, active config")
	}
	if got := cfg.MaskedToken(); strings.Contains(got, "secret") || !strings.HasPrefix(got, "8891668888:") {
		t.Fatalf("masked token leaks the secret or drops the bot id: %q", got)
	}

	// Survives a reload, and the file is 0600 (it holds live tokens).
	s2 := NewNotifyStore(path)
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := s2.Get("a1")
	if !ok || got.BotToken != cfg.BotToken || got.ChatID != cfg.ChatID || !got.Enabled {
		t.Fatalf("config did not survive reload: %#v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix permission bits -- os.Chmod there only toggles
	// the read-only attribute, so a 0666-looking mode is expected and the
	// 0600 guarantee is only assertable where the platform honors it.
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("notify.json mode = %o, want 600", perm)
		}
	}

	// Incomplete entries are refused rather than silently stored.
	if err := s.Set("a2", HostNotifyConfig{BotToken: "1:x"}); err == nil {
		t.Fatal("expected Set to reject an entry with no chat id")
	}

	if err := s.Delete("a1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.Get("a1"); ok {
		t.Fatal("expected the entry to be gone")
	}
	s3 := NewNotifyStore(path)
	if err := s3.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := s3.Get("a1"); ok {
		t.Fatal("expected the deletion to be persisted")
	}
	if err := s.Delete("a1"); err != nil {
		t.Fatalf("deleting twice should be a no-op, got %v", err)
	}
}

func TestMaybeNotifyUpdateSendsPerHostOnNewPackages(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "raspberrypi", "tok")

	store := NewNotifyStore(filepath.Join(t.TempDir(), "notify.json"))
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("a1", HostNotifyConfig{BotToken: "123456789:secret", ChatID: "42", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s.SetNotifyStore(store)

	fake := newFakeNotifier()
	var gotToken, gotChat string
	s.newTelegram = func(token, chatID string) notifier.Notifier {
		gotToken, gotChat = token, chatID
		return fake
	}

	start := time.Now().Add(-time.Hour)
	first := checker.Status{Hostname: "raspberrypi", CheckedAt: start}
	first.Packages.UpgradableTotal = 3
	if rec := doJSON(t, s, http.MethodPost, "/report", first, agentHeaders()); rec.Code != http.StatusOK {
		t.Fatalf("first report: %d %s", rec.Code, rec.Body.String())
	}
	// No previous report => no diff => silence (same rule the agent uses).
	if evs := fake.snapshot(); len(evs) != 0 {
		t.Fatalf("expected no notification for the baseline report, got %#v", evs)
	}

	second := checker.Status{Hostname: "raspberrypi", CheckedAt: start.Add(time.Minute)}
	second.Packages.UpgradableTotal = 5
	second.Packages.UpgradableSecurity = 1
	if rec := doJSON(t, s, http.MethodPost, "/report", second, agentHeaders()); rec.Code != http.StatusOK {
		t.Fatalf("second report: %d %s", rec.Code, rec.Body.String())
	}

	ev := fake.waitForEvent(t)
	if ev.Hostname != "raspberrypi" {
		t.Fatalf("event hostname = %q", ev.Hostname)
	}
	joined := strings.Join(ev.Changes, "; ")
	if !strings.Contains(joined, "2 new package update(s)") || !strings.Contains(joined, "1 new security update(s)") {
		t.Fatalf("unexpected changes: %v", ev.Changes)
	}
	if gotToken != "123456789:secret" || gotChat != "42" {
		t.Fatalf("sent with token %q chat %q, want the host's own", gotToken, gotChat)
	}
}

func TestMaybeNotifyUpdateSilentWithoutOwnBotOrWhenPaused(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "raspberrypi", "tok")

	store := NewNotifyStore(filepath.Join(t.TempDir(), "notify.json"))
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	s.SetNotifyStore(store)

	fake := newFakeNotifier()
	s.newTelegram = func(string, string) notifier.Notifier { return fake }

	start := time.Now().Add(-time.Hour)
	base := checker.Status{Hostname: "raspberrypi", CheckedAt: start}
	base.Packages.UpgradableTotal = 1
	doJSON(t, s, http.MethodPost, "/report", base, agentHeaders())

	// Paused: stored, but the host is meant to use the fleet-wide bot.
	if err := store.Set("a1", HostNotifyConfig{BotToken: "1:x", ChatID: "2", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	bumped := checker.Status{Hostname: "raspberrypi", CheckedAt: start.Add(time.Minute)}
	bumped.Packages.UpgradableTotal = 9
	doJSON(t, s, http.MethodPost, "/report", bumped, agentHeaders())

	// give the (possible) goroutine a chance to run before asserting
	time.Sleep(50 * time.Millisecond)
	if evs := fake.snapshot(); len(evs) != 0 {
		t.Fatalf("expected no per-host notification when paused, got %#v", evs)
	}
}

func TestNotificationsPageRendersMaskedToken(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "raspberrypi", "tok")

	store := NewNotifyStore(filepath.Join(t.TempDir(), "notify.json"))
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("a1", HostNotifyConfig{BotToken: "8891668888:AAH4xQsecretw2lE", ChatID: "-1001234567890", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s.SetNotifyStore(store)

	rec := doJSON(t, s, http.MethodGet, "/admin/notifications", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "raspberrypi") {
		t.Fatal("page should list the approved host")
	}
	if strings.Contains(body, "AAH4xQsecretw2lE") {
		t.Fatal("page must never echo the full bot token back")
	}
	if !strings.Contains(body, "8891668888:…w2lE") {
		t.Fatalf("page should show the masked token, got: %s", body)
	}
	// The instructions the user asked for.
	for _, want := range []string{"BotFather", "getUpdates", "chat.id"} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

func TestNotificationsSaveTestAndClear(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "raspberrypi", "tok")

	store := NewNotifyStore(filepath.Join(t.TempDir(), "notify.json"))
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	s.SetNotifyStore(store)
	fake := newFakeNotifier()
	s.newTelegram = func(string, string) notifier.Notifier { return fake }

	// Save
	rec := doForm(t, s, url.Values{
		"agent_id": {"a1"}, "action": {"save"},
		"bot_token": {"123456789:secret"}, "chat_id": {"-1001234567890"}, "enabled": {"on"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save: status %d (%s)", rec.Code, rec.Body.String())
	}
	cfg, ok := store.Get("a1")
	if !ok || !cfg.Active() || cfg.ChatID != "-1001234567890" {
		t.Fatalf("save did not persist an active config: %#v", cfg)
	}

	// Blank token keeps the stored one (the page only shows a mask).
	rec = doForm(t, s, url.Values{
		"agent_id": {"a1"}, "action": {"save"},
		"bot_token": {""}, "chat_id": {"42"}, "enabled": {"on"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save: status %d", rec.Code)
	}
	cfg, _ = store.Get("a1")
	if cfg.BotToken != "123456789:secret" || cfg.ChatID != "42" {
		t.Fatalf("blank token should keep the stored secret: %#v", cfg)
	}

	// Invalid chat id is rejected, not stored.
	rec = doForm(t, s, url.Values{
		"agent_id": {"a1"}, "action": {"save"},
		"bot_token": {"123456789:secret"}, "chat_id": {"not-a-number"}, "enabled": {"on"},
	})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Fatalf("expected an error redirect, got %q", loc)
	}
	if cfg, _ := store.Get("a1"); cfg.ChatID != "42" {
		t.Fatalf("invalid save must not overwrite the stored config: %#v", cfg)
	}

	// Test send
	rec = doForm(t, s, url.Values{
		"agent_id": {"a1"}, "action": {"test"},
		"bot_token": {"123456789:secret"}, "chat_id": {"42"}, "enabled": {"on"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("test: status %d", rec.Code)
	}
	ev := fake.waitForEvent(t)
	if ev.Title != "test notification" || ev.Hostname != "raspberrypi" {
		t.Fatalf("unexpected test event: %#v", ev)
	}

	// Clear
	rec = doForm(t, s, url.Values{"agent_id": {"a1"}, "action": {"clear"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("clear: status %d", rec.Code)
	}
	if _, ok := store.Get("a1"); ok {
		t.Fatal("clear should have removed the config")
	}

	// Test on an unconfigured host persists the values: the page has no
	// client-side state, so a test that didn't save would throw away
	// everything just typed when the redirect re-rendered.
	rec = doForm(t, s, url.Values{
		"agent_id": {"a1"}, "action": {"test"},
		"bot_token": {"987654321:other"}, "chat_id": {"7"}, "enabled": {"on"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("test: status %d", rec.Code)
	}
	if cfg, ok := store.Get("a1"); !ok || cfg.BotToken != "987654321:other" || cfg.ChatID != "7" || !cfg.Active() {
		t.Fatalf("test should have saved the typed values, got %#v (present=%v)", cfg, ok)
	}
}

func TestNotificationsRejectsUnknownHost(t *testing.T) {
	s, _ := newTestServer(t)
	store := NewNotifyStore(filepath.Join(t.TempDir(), "notify.json"))
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	s.SetNotifyStore(store)

	rec := doForm(t, s, url.Values{"agent_id": {"nope"}, "action": {"save"}, "bot_token": {"1:x"}, "chat_id": {"2"}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Fatalf("expected an error redirect for an unknown host, got %q", loc)
	}
	if len(store.Snapshot()) != 0 {
		t.Fatal("nothing should have been stored")
	}
}

func agentHeaders() map[string]string {
	return map[string]string{"X-Agent-ID": "a1", "Authorization": "Bearer tok"}
}

func doForm(t *testing.T, s *Server, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/notifications", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}
