package aggregator

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"update-detector/internal/checker"
	"update-detector/internal/notifier"
)

type captureNotifier struct {
	events []notifier.Event
}

func (c *captureNotifier) Name() string { return "capture" }

func (c *captureNotifier) Send(_ context.Context, ev notifier.Event) error {
	c.events = append(c.events, ev)
	return nil
}

func TestResultTitleNamesTheAction(t *testing.T) {
	cases := []struct {
		name    string
		action  ActionType
		known   bool
		success bool
		want    string
	}{
		{"recheck", ActionRecheck, true, true, "recheck"},
		{"failed recheck", ActionRecheck, true, false, "recheck failed"},
		{"upgrade", ActionUpgrade, true, true, "update applied"},
		{"failed upgrade", ActionUpgrade, true, false, "update failed"},
		{"full upgrade", ActionFullUpgrade, true, true, "update applied"},
		{"packages", ActionPackages, true, true, "package update applied"},
		{"self-update", ActionSelfUpdate, true, true, "self-update applied"},
		{"companion swap", ActionCompleteCompanionSwap, true, true, "companion update applied"},
		// Unknown type (e.g. tracking lost across a restart): keep the
		// historical wording rather than inventing a label.
		{"unknown type, success", ActionType("mystery"), false, true, "update applied"},
		{"unknown type, failure", ActionType("mystery"), false, false, "update failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resultTitle(tc.action, tc.known, tc.success); got != tc.want {
				t.Fatalf("resultTitle(%q, known=%v, success=%v) = %q, want %q",
					tc.action, tc.known, tc.success, got, tc.want)
			}
		})
	}
}

func TestResultEventOmitsStaleStatus(t *testing.T) {
	stale := checker.Status{Hostname: "web01"}
	stale.Packages.UpgradableTotal = 12
	rec := AgentRecord{
		ID:         "a1",
		Hostname:   "web01",
		LastReport: &stale,
	}

	ev := resultEvent(rec, ActionRecheck, true, true, "recheck triggered")
	if !ev.Brief {
		t.Fatal("result events must be Brief so the stale upgradable/reboot trailer is not rendered")
	}
	if len(ev.Changes) != 1 || ev.Changes[0] != "recheck triggered" {
		t.Fatalf("changes = %#v, want the action detail only", ev.Changes)
	}
	if ev.Status.Packages.UpgradableTotal != 0 {
		t.Fatalf("status leaked into the event: %#v", ev.Status)
	}
	if ev.ApplyAgentID != "" {
		t.Fatalf("action results must not offer an apply button, got %q", ev.ApplyAgentID)
	}
}

func TestHandleCompanionResultNotifiesWithPendingActionType(t *testing.T) {
	cap := &captureNotifier{}
	reg := NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	hub := NewCompanionHub()
	s := NewServer(context.Background(), reg, hub, notifier.NewManager(cap), "", nil, NewOutputHub())

	if _, _, err := reg.Enroll("a1", "web01", "tok"); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if err := reg.SetStatus("a1", StatusApproved); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	res := hub.Connect("a1", KindCompanion, "")
	defer hub.Disconnect("a1", res.Ch)

	actionID, _, err := s.PushApply("a1", ActionUpgrade, nil)
	if err != nil {
		t.Fatalf("PushApply: %v", err)
	}
	select {
	case <-res.Ch:
	case <-time.After(time.Second):
		t.Fatal("action never arrived on the companion stream")
	}

	rec := doJSON(t, s, http.MethodPost, "/companion/result",
		companionResultRequest{ActionID: actionID, Success: true, Message: "upgrade succeeded"},
		map[string]string{"X-Agent-ID": "a1", "Authorization": "Bearer tok"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, body %s", rec.Code, rec.Body.String())
	}

	if len(cap.events) != 1 {
		t.Fatalf("got %d notifications, want 1", len(cap.events))
	}
	ev := cap.events[0]
	if ev.Title != "update applied" {
		t.Fatalf("title = %q, want %q", ev.Title, "update applied")
	}
	if !ev.Brief {
		t.Fatal("expected a Brief event (no stale status trailer)")
	}
	if len(ev.Changes) != 1 || ev.Changes[0] != "upgrade succeeded" {
		t.Fatalf("changes = %#v", ev.Changes)
	}
}

func TestCompanionHubPendingType(t *testing.T) {
	h := NewCompanionHub()
	h.Connect("a1", KindCompanion, "")

	if _, ok := h.PendingType("a1", "act1"); ok {
		t.Fatal("nothing is in flight yet")
	}
	if err := h.Push("a1", Action{ID: "act1", Type: ActionUpgrade, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if got, ok := h.PendingType("a1", "act1"); !ok || got != ActionUpgrade {
		t.Fatalf("PendingType = %q, %v; want %q, true", got, ok, ActionUpgrade)
	}
	if _, ok := h.PendingType("a1", "other"); ok {
		t.Fatal("a different action ID must not resolve")
	}

	h.RecordResult("a1", ActionResult{ActionID: "act1"})
	if _, ok := h.PendingType("a1", "act1"); ok {
		t.Fatal("PendingType must be cleared once the result lands")
	}
}
