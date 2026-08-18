package aggregator

import (
	"path/filepath"
	"testing"
	"time"

	"update-detector/internal/checker"
)

func newTestRegistry(t *testing.T) *Registry {
	return NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
}

func TestEnrollNewAgentIsPending(t *testing.T) {
	r := newTestRegistry(t)
	outcome, status, err := r.Enroll("agent-1", "web01", "secret-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome != EnrollCreatedPending || status != StatusPending {
		t.Fatalf("got outcome=%v status=%v, want CreatedPending/pending", outcome, status)
	}
}

func TestEnrollIdempotentSameToken(t *testing.T) {
	r := newTestRegistry(t)
	if _, _, err := r.Enroll("agent-1", "web01", "secret-token"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetStatus("agent-1", StatusApproved); err != nil {
		t.Fatal(err)
	}

	outcome, status, err := r.Enroll("agent-1", "web01-renamed", "secret-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome != EnrollAlreadyKnown || status != StatusApproved {
		t.Fatalf("got outcome=%v status=%v, want AlreadyKnown/approved", outcome, status)
	}

	rec, ok := r.Get("agent-1")
	if !ok || rec.Hostname != "web01-renamed" {
		t.Fatalf("expected hostname to be refreshed, got %#v", rec)
	}
}

func TestEnrollConflictOnDifferentToken(t *testing.T) {
	r := newTestRegistry(t)
	if _, _, err := r.Enroll("agent-1", "web01", "secret-token"); err != nil {
		t.Fatal(err)
	}

	outcome, _, err := r.Enroll("agent-1", "web01", "a-different-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome != EnrollConflict {
		t.Fatalf("got outcome=%v, want Conflict", outcome)
	}
}

func TestReportRequiresApproval(t *testing.T) {
	r := newTestRegistry(t)
	if _, _, err := r.Enroll("agent-1", "web01", "secret-token"); err != nil {
		t.Fatal(err)
	}

	outcome, err := r.Report("agent-1", "secret-token", checker.Status{Hostname: "web01"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome != ReportNotApproved {
		t.Fatalf("got %v, want ReportNotApproved", outcome)
	}

	if err := r.SetStatus("agent-1", StatusApproved); err != nil {
		t.Fatal(err)
	}

	outcome, err = r.Report("agent-1", "secret-token", checker.Status{Hostname: "web01", OK: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome != ReportAccepted {
		t.Fatalf("got %v, want ReportAccepted", outcome)
	}

	rec, ok := r.Get("agent-1")
	if !ok || rec.LastReport == nil || !rec.LastReport.OK {
		t.Fatalf("expected last report to be stored, got %#v", rec)
	}
}

func TestReportRejectsWrongToken(t *testing.T) {
	r := newTestRegistry(t)
	if _, _, err := r.Enroll("agent-1", "web01", "secret-token"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetStatus("agent-1", StatusApproved); err != nil {
		t.Fatal(err)
	}

	outcome, err := r.Report("agent-1", "wrong-token", checker.Status{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome != ReportUnauthorized {
		t.Fatalf("got %v, want ReportUnauthorized", outcome)
	}
}

func TestReportUnknownAgent(t *testing.T) {
	r := newTestRegistry(t)
	outcome, err := r.Report("does-not-exist", "token", checker.Status{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome != ReportUnknownAgent {
		t.Fatalf("got %v, want ReportUnknownAgent", outcome)
	}
}

func TestSetStatusNotFound(t *testing.T) {
	r := newTestRegistry(t)
	if err := r.SetStatus("nope", StatusApproved); err != ErrNotFound {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestForgetRemovesEntryRegardlessOfStatus(t *testing.T) {
	r := newTestRegistry(t)
	if _, _, err := r.Enroll("agent-1", "web01", "token-1"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetStatus("agent-1", StatusRejected); err != nil {
		t.Fatal(err)
	}

	if err := r.Forget("agent-1"); err != nil {
		t.Fatalf("Forget failed: %v", err)
	}
	if _, ok := r.Get("agent-1"); ok {
		t.Fatal("expected agent-1 to be gone after Forget")
	}

	// Re-enrolling afterward starts over as pending, not blocked by the
	// deleted record somehow lingering.
	_, status, err := r.Enroll("agent-1", "web01", "token-1")
	if err != nil {
		t.Fatal(err)
	}
	if status != StatusPending {
		t.Fatalf("got status %v after re-enrolling a forgotten agent, want StatusPending", status)
	}
}

func TestForgetNotFound(t *testing.T) {
	r := newTestRegistry(t)
	if err := r.Forget("nope"); err != ErrNotFound {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestForgetPersistsAcrossLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	r1 := NewRegistry(path)
	if _, _, err := r1.Enroll("agent-1", "web01", "token-1"); err != nil {
		t.Fatal(err)
	}
	if err := r1.Forget("agent-1"); err != nil {
		t.Fatal(err)
	}

	r2 := NewRegistry(path)
	if err := r2.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if _, ok := r2.Get("agent-1"); ok {
		t.Fatal("expected agent-1 to stay gone after reloading from disk")
	}
}

func TestFindApprovedByHostnamePicksMostRecentlySeen(t *testing.T) {
	r := newTestRegistry(t)
	for _, id := range []string{"agent-1", "agent-2"} {
		if _, _, err := r.Enroll(id, "web01", "token-"+id); err != nil {
			t.Fatal(err)
		}
		if err := r.SetStatus(id, StatusApproved); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := r.Report("agent-1", "token-agent-1", checker.Status{Packages: checker.PackageInfo{UpgradableTotal: 1}}); err != nil {
		t.Fatal(err)
	}
	// FindApprovedByHostname breaks ties via LastSeen.After, strictly --
	// two Report calls back-to-back with no delay can land on the exact
	// same time.Now() value on a platform with coarse timer resolution
	// (confirmed live: Windows' default tick, unlike Linux's much finer
	// one, ties often enough to flip this test's own outcome). This
	// test's actual intent is "the more recently reported one wins," not
	// tie-breaking behavior on a collision, so force a real gap instead.
	time.Sleep(50 * time.Millisecond)
	if _, err := r.Report("agent-2", "token-agent-2", checker.Status{Packages: checker.PackageInfo{UpgradableTotal: 2}}); err != nil {
		t.Fatal(err)
	}

	rec, ok := r.FindApprovedByHostname("web01")
	if !ok {
		t.Fatal("expected to find an approved agent for web01")
	}
	if rec.ID != "agent-2" {
		t.Fatalf("got id %q, want agent-2 (most recently seen)", rec.ID)
	}
}

func TestAuthenticate(t *testing.T) {
	r := newTestRegistry(t)
	if _, _, err := r.Enroll("agent-1", "web01", "secret-token"); err != nil {
		t.Fatal(err)
	}

	if _, outcome := r.Authenticate("does-not-exist", "secret-token"); outcome != AuthUnknownAgent {
		t.Fatalf("got %v, want AuthUnknownAgent", outcome)
	}
	if _, outcome := r.Authenticate("agent-1", "wrong-token"); outcome != AuthUnauthorized {
		t.Fatalf("got %v, want AuthUnauthorized", outcome)
	}
	if _, outcome := r.Authenticate("agent-1", "secret-token"); outcome != AuthNotApproved {
		t.Fatalf("got %v, want AuthNotApproved before approval", outcome)
	}

	if err := r.SetStatus("agent-1", StatusApproved); err != nil {
		t.Fatal(err)
	}
	rec, outcome := r.Authenticate("agent-1", "secret-token")
	if outcome != AuthOK || rec.ID != "agent-1" {
		t.Fatalf("got rec=%#v outcome=%v, want AuthOK for agent-1", rec, outcome)
	}
}

func TestRegistryPersistsAcrossLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	r1 := NewRegistry(path)
	if _, _, err := r1.Enroll("agent-1", "web01", "secret-token"); err != nil {
		t.Fatal(err)
	}
	if err := r1.SetStatus("agent-1", StatusApproved); err != nil {
		t.Fatal(err)
	}

	r2 := NewRegistry(path)
	if err := r2.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	rec, ok := r2.Get("agent-1")
	if !ok || rec.Status != StatusApproved || rec.Hostname != "web01" {
		t.Fatalf("got %#v, want approved web01 record", rec)
	}
}
