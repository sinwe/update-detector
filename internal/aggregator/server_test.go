package aggregator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"update-detector/internal/checker"
	"update-detector/internal/notifier"
	"update-detector/internal/selfupdate"
	"update-detector/internal/version"
)

func newTestServer(t *testing.T) (*Server, *Registry) {
	return newTestServerWithSecret(t, "")
}

func newTestServerWithSecret(t *testing.T, adminApplySecret string) (*Server, *Registry) {
	reg := NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	hub := NewCompanionHub()
	return NewServer(context.Background(), reg, hub, notifier.NewManager(), adminApplySecret, nil, NewOutputHub()), reg
}

// newTestServerWithLatestVersion is like newTestServerWithSecret, but
// with a real *selfupdate.Client already refreshed (against a throwaway
// fake Forgejo server) to report latestVersion as the newest release --
// for exercising the admin page's update-available banner/buttons.
func newTestServerWithLatestVersion(t *testing.T, adminApplySecret, latestVersion string) (*Server, *Registry) {
	t.Helper()
	fakeForgejo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]string{{"tag_name": latestVersion}})
	}))
	t.Cleanup(fakeForgejo.Close)

	client := selfupdate.New(fakeForgejo.URL, false)
	if err := client.Refresh(context.Background()); err != nil {
		t.Fatalf("refreshing fake selfupdate client: %v", err)
	}

	reg := NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	hub := NewCompanionHub()
	return NewServer(context.Background(), reg, hub, notifier.NewManager(), adminApplySecret, client, NewOutputHub()), reg
}

func doJSON(t *testing.T, s *Server, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHandleEnrollNewAgent(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPost, "/enroll", enrollRequest{AgentID: "a1", Hostname: "web01", Token: "tok"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["status"] != "pending" {
		t.Fatalf("got status %q, want pending", resp["status"])
	}
}

func TestHandleEnrollConflict(t *testing.T) {
	s, _ := newTestServer(t)
	doJSON(t, s, http.MethodPost, "/enroll", enrollRequest{AgentID: "a1", Hostname: "web01", Token: "tok"}, nil)
	rec := doJSON(t, s, http.MethodPost, "/enroll", enrollRequest{AgentID: "a1", Hostname: "web01", Token: "different"}, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got status %d, want 409", rec.Code)
	}
}

func TestHandleReportFlow(t *testing.T) {
	s, reg := newTestServer(t)
	doJSON(t, s, http.MethodPost, "/enroll", enrollRequest{AgentID: "a1", Hostname: "web01", Token: "tok"}, nil)

	// pending agent cannot report yet
	rec := doJSON(t, s, http.MethodPost, "/report", checker.Status{Hostname: "web01"}, map[string]string{
		"X-Agent-ID":    "a1",
		"Authorization": "Bearer tok",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got status %d, want 403 before approval", rec.Code)
	}

	if err := reg.SetStatus("a1", StatusApproved); err != nil {
		t.Fatal(err)
	}

	rec = doJSON(t, s, http.MethodPost, "/report", checker.Status{Hostname: "web01", OK: true}, map[string]string{
		"X-Agent-ID":    "a1",
		"Authorization": "Bearer tok",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, body %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/report", checker.Status{Hostname: "web01"}, map[string]string{
		"X-Agent-ID":    "a1",
		"Authorization": "Bearer wrong-token",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401 for wrong token", rec.Code)
	}
}

func TestHandleAdminApproveRejectFlow(t *testing.T) {
	s, _ := newTestServer(t)
	doJSON(t, s, http.MethodPost, "/enroll", enrollRequest{AgentID: "a1", Hostname: "web01", Token: "tok"}, nil)

	adminBody := func() string {
		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Body.String()
	}

	if body := adminBody(); !strings.Contains(body, "web01") {
		t.Fatalf("expected pending agent listed in admin page, got: %s", body)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/agents/a1/approve", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("got status %d, want redirect", rec.Code)
	}

	rec2 := doJSON(t, s, http.MethodGet, "/widgets/hosts", nil, nil)
	var hosts []hostSummary
	if err := json.Unmarshal(rec2.Body.Bytes(), &hosts); err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].Hostname != "web01" {
		t.Fatalf("expected web01 to be listed as approved, got %#v", hosts)
	}
}

func TestHandleAdminPageShowsPackages(t *testing.T) {
	s, reg := newTestServer(t)
	doJSON(t, s, http.MethodPost, "/enroll", enrollRequest{AgentID: "a1", Hostname: "web01", Token: "tok"}, nil)
	if err := reg.SetStatus("a1", StatusApproved); err != nil {
		t.Fatal(err)
	}
	doJSON(t, s, http.MethodPost, "/report", checker.Status{
		Hostname: "web01",
		Packages: checker.PackageInfo{
			UpgradableTotal:    1,
			UpgradableSecurity: 1,
			Upgrades: []checker.PackageUpgrade{
				{Name: "curl", CurrentVersion: "7.81.0-1", CandidateVersion: "7.81.0-2", Security: true},
			},
		},
	}, map[string]string{"X-Agent-ID": "a1", "Authorization": "Bearer tok"})

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()

	if !strings.Contains(body, "curl") {
		t.Fatalf("expected package name in admin page, got: %s", body)
	}
	if !strings.Contains(body, "/widgets/hosts/web01") {
		t.Fatalf("expected a link to the host's raw JSON, got: %s", body)
	}
	if !strings.Contains(body, "/widgets/packages") {
		t.Fatalf("expected a fleet-wide packages link, got: %s", body)
	}
}

func TestHandleWidgetSummaryAndHost(t *testing.T) {
	s, reg := newTestServer(t)
	doJSON(t, s, http.MethodPost, "/enroll", enrollRequest{AgentID: "a1", Hostname: "web01", Token: "tok"}, nil)
	if err := reg.SetStatus("a1", StatusApproved); err != nil {
		t.Fatal(err)
	}
	doJSON(t, s, http.MethodPost, "/report", checker.Status{
		Hostname:       "web01",
		OK:             false,
		RebootRequired: true,
		Packages:       checker.PackageInfo{UpgradableTotal: 3, UpgradableSecurity: 1},
	}, map[string]string{"X-Agent-ID": "a1", "Authorization": "Bearer tok"})

	rec := doJSON(t, s, http.MethodGet, "/widgets/summary", nil, nil)
	var summary summaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.HostsTotal != 1 || summary.HostsReporting != 1 || summary.HostsOK != 0 || summary.HostsRebootRequired != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if summary.PackagesUpgradableTotal != 3 || summary.PackagesUpgradableSecurity != 1 {
		t.Fatalf("unexpected package totals: %#v", summary)
	}

	rec = doJSON(t, s, http.MethodGet, "/widgets/hosts/web01", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, body %s", rec.Code, rec.Body.String())
	}
	var status checker.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Hostname != "web01" || status.Packages.UpgradableTotal != 3 {
		t.Fatalf("unexpected host status: %#v", status)
	}

	rec = doJSON(t, s, http.MethodGet, "/widgets/hosts/unknown-host", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404 for unknown host", rec.Code)
	}
}

func TestHandleWidgetPackages(t *testing.T) {
	s, reg := newTestServer(t)
	doJSON(t, s, http.MethodPost, "/enroll", enrollRequest{AgentID: "a1", Hostname: "web01", Token: "tok"}, nil)
	if err := reg.SetStatus("a1", StatusApproved); err != nil {
		t.Fatal(err)
	}
	doJSON(t, s, http.MethodPost, "/report", checker.Status{
		Hostname: "web01",
		Packages: checker.PackageInfo{
			UpgradableTotal:    2,
			UpgradableSecurity: 1,
			Upgrades: []checker.PackageUpgrade{
				{Name: "curl", CandidateVersion: "7.81.0-1ubuntu1.16", Security: true},
				{Name: "vim", CandidateVersion: "2:9.0.0-1"},
			},
		},
	}, map[string]string{"X-Agent-ID": "a1", "Authorization": "Bearer tok"})

	rec := doJSON(t, s, http.MethodGet, "/widgets/packages", nil, nil)
	var pkgs []pendingPackage
	if err := json.Unmarshal(rec.Body.Bytes(), &pkgs); err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("got %d packages, want 2: %#v", len(pkgs), pkgs)
	}
	for _, p := range pkgs {
		if p.Hostname != "web01" {
			t.Fatalf("expected hostname web01 on every entry, got %#v", p)
		}
	}

	rec = doJSON(t, s, http.MethodGet, "/widgets/packages?security=true", nil, nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &pkgs); err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 || pkgs[0].Name != "curl" {
		t.Fatalf("expected only curl with security=true filter, got %#v", pkgs)
	}
}

func TestHandleOpenAPISpec(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/openapi.yaml", nil, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/yaml" {
		t.Fatalf("got Content-Type %q, want application/yaml", got)
	}
	if !strings.Contains(rec.Body.String(), "openapi: 3.0.3") {
		t.Fatalf("body doesn't look like an OpenAPI spec: %s", rec.Body.String())
	}
}

func approvedAgent(t *testing.T, s *Server, reg *Registry, id, hostname, token string) {
	t.Helper()
	doJSON(t, s, http.MethodPost, "/enroll", enrollRequest{AgentID: id, Hostname: hostname, Token: token}, nil)
	if err := reg.SetStatus(id, StatusApproved); err != nil {
		t.Fatal(err)
	}
}

func TestHandleCompanionStreamRequiresAuth(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	rec := doJSON(t, s, http.MethodGet, "/companion/stream", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401 with no credentials", rec.Code)
	}

	rec = doJSON(t, s, http.MethodGet, "/companion/stream", nil, map[string]string{
		"X-Agent-ID": "a1", "Authorization": "Bearer wrong",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401 with wrong token", rec.Code)
	}
}

func TestHandleCompanionStreamPushesAction(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	httpSrv := httptest.NewServer(s.Handler())
	defer httpSrv.Close()

	req, err := http.NewRequest(http.MethodGet, httpSrv.URL+"/companion/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Agent-ID", "a1")
	req.Header.Set("Authorization", "Bearer tok")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.StatusCode)
	}

	deadline := time.Now().Add(2 * time.Second)
	for !s.hub.Connected("a1") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !s.hub.Connected("a1") {
		t.Fatal("companion never showed as connected")
	}

	if err := s.hub.Push("a1", Action{ID: "act1", Type: ActionUpgrade, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	buf := make([]byte, 4096)
	n, err := resp.Body.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf[:n]), `"id":"act1"`) {
		t.Fatalf("expected pushed action in SSE body, got: %q", buf[:n])
	}
}

// TestHandleCompanionStreamRecordsAggregatorPresentHeader covers the
// wiring for the "hide Update aggregator on hosts that don't run it" fix:
// a real companion connection carrying X-Aggregator-Present must update
// the hub, but an agent-kind connection (which never runs the
// aggregator-colocation check at all -- see AggregatorColocated) must not,
// even if the header happens to be present.
func TestHandleCompanionStreamRecordsAggregatorPresentHeader(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	httpSrv := httptest.NewServer(s.Handler())
	defer httpSrv.Close()

	connect := func(clientKind, aggregatorPresent string) *http.Response {
		req, err := http.NewRequest(http.MethodGet, httpSrv.URL+"/companion/stream", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Agent-ID", "a1")
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("X-Client-Kind", clientKind)
		req.Header.Set("X-Aggregator-Present", aggregatorPresent)
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := connect("companion", "true")
	defer resp.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for !s.hub.Connected("a1") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !s.hub.AggregatorPresent("a1") {
		t.Fatal("expected AggregatorPresent to be true after a companion connects with X-Aggregator-Present: true")
	}

	// An agent-kind connect is now accepted alongside the companion
	// (routed to agentStreams) so the agent can receive agent-only
	// actions like ActionCompleteCompanionSwap. Confirm that this
	// agent connection did not touch AggregatorPresent, even though
	// it carried the header too -- only companion connections set it.
	resp2 := connect("agent", "true")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want 200 for an agent-kind connect alongside a companion", resp2.StatusCode)
	}
	if !s.hub.AggregatorPresent("a1") {
		t.Fatal("expected AggregatorPresent to remain true -- an agent-kind connect must not clear it")
	}
}

func TestHandleCompanionOutputRequiresActionID(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")
	rec := doJSON(t, s, http.MethodPost, "/companion/output", nil, map[string]string{"X-Agent-ID": "a1", "Authorization": "Bearer tok"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 for missing action_id", rec.Code)
	}
}

func TestHandleCompanionOutputRejectsMismatchedActionID(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")
	res := s.hub.Connect("a1", KindCompanion, "v0.0.0-test")
	defer s.hub.Disconnect("a1", res.Ch)
	if err := s.hub.Push("a1", Action{ID: "act1", Type: ActionUpgrade, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	rec := doJSON(t, s, http.MethodPost, "/companion/output?action_id=wrong", nil, map[string]string{"X-Agent-ID": "a1", "Authorization": "Bearer tok"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("got status %d, want 409 for an action_id that doesn't match this agent's in-flight action", rec.Code)
	}
}

// TestHandleCompanionOutputFansOutToAdminStream is the end-to-end path:
// a companion's chunked /companion/output POST must reach a concurrent
// browser subscriber on /admin/agents/{id}/output/stream as a "line" SSE
// event, mirroring TestHandleCompanionStreamPushesAction's own real-server
// + real-client shape (an httptest.ResponseRecorder can't do real
// streaming).
func TestHandleCompanionOutputFansOutToAdminStream(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")
	res := s.hub.Connect("a1", KindCompanion, "v0.0.0-test")
	defer s.hub.Disconnect("a1", res.Ch)
	if err := s.hub.Push("a1", Action{ID: "act1", Type: ActionUpgrade, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("push failed: %v", err)
	}
	// In production, Begin happens inside the admin handlers (handleAdminApply
	// et al.) right after a successful Push -- this test calls hub.Push
	// directly, bypassing those, so it must simulate that same step.
	s.outputHub.Begin("a1", "act1")

	httpSrv := httptest.NewServer(s.Handler())
	defer httpSrv.Close()

	streamReq, err := http.NewRequest(http.MethodGet, httpSrv.URL+"/admin/agents/a1/output/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	streamResp, err := (&http.Client{Timeout: 5 * time.Second}).Do(streamReq)
	if err != nil {
		t.Fatal(err)
	}
	defer streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want 200", streamResp.StatusCode)
	}

	pr, pw := io.Pipe()
	go func() {
		_ = json.NewEncoder(pw).Encode(companionOutputFrame{ActionID: "act1", Line: "hello"})
		pw.Close()
	}()

	outputReq, err := http.NewRequest(http.MethodPost, httpSrv.URL+"/companion/output?action_id=act1", pr)
	if err != nil {
		t.Fatal(err)
	}
	outputReq.Header.Set("X-Agent-ID", "a1")
	outputReq.Header.Set("Authorization", "Bearer tok")
	outputResp, err := (&http.Client{Timeout: 5 * time.Second}).Do(outputReq)
	if err != nil {
		t.Fatal(err)
	}
	defer outputResp.Body.Close()
	if outputResp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want 200", outputResp.StatusCode)
	}

	buf := make([]byte, 4096)
	n, err := streamResp.Body.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	got := string(buf[:n])
	if !strings.Contains(got, "event: line") || !strings.Contains(got, "hello") {
		t.Fatalf("expected a line event mentioning hello in SSE body, got: %q", got)
	}
}

// TestHandleCompanionOutputEndsAsDisconnectedWithoutPriorResult is the
// regression test for the companion-self-update-restart case: the output
// stream's body ending with no prior handleCompanionResult call must
// surface as "disconnected" to browser subscribers, not silence.
func TestHandleCompanionOutputEndsAsDisconnectedWithoutPriorResult(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")
	res := s.hub.Connect("a1", KindCompanion, "v0.0.0-test")
	defer s.hub.Disconnect("a1", res.Ch)
	if err := s.hub.Push("a1", Action{ID: "act1", Type: ActionUpgrade, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("push failed: %v", err)
	}
	// In production, Begin happens inside the admin handlers (handleAdminApply
	// et al.) right after a successful Push -- this test calls hub.Push
	// directly, bypassing those, so it must simulate that same step.
	s.outputHub.Begin("a1", "act1")

	httpSrv := httptest.NewServer(s.Handler())
	defer httpSrv.Close()

	streamReq, err := http.NewRequest(http.MethodGet, httpSrv.URL+"/admin/agents/a1/output/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	streamResp, err := (&http.Client{Timeout: 5 * time.Second}).Do(streamReq)
	if err != nil {
		t.Fatal(err)
	}
	defer streamResp.Body.Close()

	// An empty body that ends immediately -- simulates the companion
	// process dying mid-action (e.g. self-updating itself) before ever
	// sending a single line or a final result.
	pr, pw := io.Pipe()
	pw.Close()

	outputReq, err := http.NewRequest(http.MethodPost, httpSrv.URL+"/companion/output?action_id=act1", pr)
	if err != nil {
		t.Fatal(err)
	}
	outputReq.Header.Set("X-Agent-ID", "a1")
	outputReq.Header.Set("Authorization", "Bearer tok")
	outputResp, err := (&http.Client{Timeout: 5 * time.Second}).Do(outputReq)
	if err != nil {
		t.Fatal(err)
	}
	defer outputResp.Body.Close()

	buf := make([]byte, 4096)
	n, err := streamResp.Body.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	got := string(buf[:n])
	if !strings.Contains(got, "event: disconnected") {
		t.Fatalf("expected a disconnected event, got: %q", got)
	}
}

// TestHandleCompanionResultEndsOutputStreamAsDone confirms the normal,
// successful path: recording a result via handleCompanionResult ends the
// agent's output stream as "done" for browser subscribers.
func TestHandleCompanionResultEndsOutputStreamAsDone(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")
	res := s.hub.Connect("a1", KindCompanion, "v0.0.0-test")
	defer s.hub.Disconnect("a1", res.Ch)
	if err := s.hub.Push("a1", Action{ID: "act1", Type: ActionUpgrade, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("push failed: %v", err)
	}
	// Simulates the output stream already being active for this action --
	// in reality, this is what handleCompanionOutput's own POST does
	// before handleCompanionResult ever runs.
	s.outputHub.Begin("a1", "act1")

	ch, cancel := s.outputHub.Subscribe("a1")
	defer cancel()

	rec := doJSON(t, s, http.MethodPost, "/companion/result", companionResultRequest{ActionID: "act1", Success: true}, map[string]string{"X-Agent-ID": "a1", "Authorization": "Bearer tok"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}

	select {
	case ev := <-ch:
		if ev.Kind != EventDone {
			t.Fatalf("got %#v, want EventDone", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the done event")
	}
}

// TestHandleCompanionStreamMissingKindHeaderDefaultsToCompanion locks in
// backward compat: existing companion binaries predate the X-Client-Kind
// header entirely, and must still be treated as a companion connection
// (able to receive apply-type actions), not rejected or downgraded.
func TestHandleCompanionStreamMissingKindHeaderDefaultsToCompanion(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	httpSrv := httptest.NewServer(s.Handler())
	defer httpSrv.Close()

	req, err := http.NewRequest(http.MethodGet, httpSrv.URL+"/companion/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Agent-ID", "a1")
	req.Header.Set("Authorization", "Bearer tok")
	// Deliberately no X-Client-Kind at all.

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.StatusCode)
	}

	deadline := time.Now().Add(2 * time.Second)
	for !s.hub.Connected("a1") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if kind, ok := s.hub.Kind("a1"); !ok || kind != KindCompanion {
		t.Fatalf("got kind=%q ok=%v, want KindCompanion when the header is absent", kind, ok)
	}
}

// TestHandleCompanionStreamAgentAcceptedAlongsideCompanion covers the
// "existing=companion, new=agent" arbitration rule end-to-end over real
// HTTP: the agent is now accepted alongside the companion (routed to
// agentStreams) so it can receive agent-only actions like
// ActionCompleteCompanionSwap. The companion's own connection must be
// completely unaffected.
func TestHandleCompanionStreamAgentAcceptedAlongsideCompanion(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	httpSrv := httptest.NewServer(s.Handler())
	defer httpSrv.Close()

	// Connect the companion first.
	compReq, err := http.NewRequest(http.MethodGet, httpSrv.URL+"/companion/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	compReq.Header.Set("X-Agent-ID", "a1")
	compReq.Header.Set("Authorization", "Bearer tok")
	compReq.Header.Set("X-Client-Kind", "companion")

	client := &http.Client{Timeout: 5 * time.Second}
	companionResp, err := client.Do(compReq)
	if err != nil {
		t.Fatal(err)
	}
	defer companionResp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for !s.hub.Connected("a1") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !s.hub.Connected("a1") {
		t.Fatal("companion never showed as connected")
	}

	// Connect the agent alongside the companion -- must be accepted
	// (200), not rejected (409). Use a real HTTP client with a timeout
	// since this is an SSE endpoint that stays open.
	agentReq, err := http.NewRequest(http.MethodGet, httpSrv.URL+"/companion/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	agentReq.Header.Set("X-Agent-ID", "a1")
	agentReq.Header.Set("Authorization", "Bearer tok")
	agentReq.Header.Set("X-Client-Kind", "agent")

	agentResp, err := client.Do(agentReq)
	if err != nil {
		t.Fatal(err)
	}
	defer agentResp.Body.Close()

	if agentResp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want 200 for an agent connecting alongside a companion", agentResp.StatusCode)
	}

	// The companion's main stream must be untouched.
	if kind, ok := s.hub.Kind("a1"); !ok || kind != KindCompanion {
		t.Fatalf("got kind=%q ok=%v, want the existing companion connection untouched", kind, ok)
	}

	// The agent stream should be in agentStreams.
	s.hub.mu.Lock()
	_, agentStreamExists := s.hub.agentStreams["a1"]
	s.hub.mu.Unlock()
	if !agentStreamExists {
		t.Fatal("expected agent stream to be in agentStreams")
	}
}

// TestHandleCompanionStreamCompanionPreemptsAgent covers the
// "existing=agent, new=companion" arbitration rule end-to-end over real
// HTTP: the agent's stream must receive one final "event: superseded"
// frame and the slot must flip to companion.
func TestHandleCompanionStreamCompanionPreemptsAgent(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	httpSrv := httptest.NewServer(s.Handler())
	defer httpSrv.Close()

	agentReq, err := http.NewRequest(http.MethodGet, httpSrv.URL+"/companion/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	agentReq.Header.Set("X-Agent-ID", "a1")
	agentReq.Header.Set("Authorization", "Bearer tok")
	agentReq.Header.Set("X-Client-Kind", "agent")

	client := &http.Client{Timeout: 5 * time.Second}
	agentResp, err := client.Do(agentReq)
	if err != nil {
		t.Fatal(err)
	}
	defer agentResp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if kind, ok := s.hub.Kind("a1"); ok && kind == KindAgent {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent never showed as connected")
		}
		time.Sleep(time.Millisecond)
	}

	companionReq, err := http.NewRequest(http.MethodGet, httpSrv.URL+"/companion/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	companionReq.Header.Set("X-Agent-ID", "a1")
	companionReq.Header.Set("Authorization", "Bearer tok")
	companionReq.Header.Set("X-Client-Kind", "companion")
	companionResp, err := client.Do(companionReq)
	if err != nil {
		t.Fatal(err)
	}
	defer companionResp.Body.Close()

	deadline = time.Now().Add(2 * time.Second)
	for {
		if kind, ok := s.hub.Kind("a1"); ok && kind == KindCompanion {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("companion never took over the slot")
		}
		time.Sleep(time.Millisecond)
	}

	buf := make([]byte, 4096)
	n, err := agentResp.Body.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf[:n]), "event: superseded") {
		t.Fatalf("expected a superseded frame on the preempted agent stream, got: %q", buf[:n])
	}
}

func TestHandleCompanionResult(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	rec := doJSON(t, s, http.MethodPost, "/companion/result", companionResultRequest{
		ActionID: "act1", Success: true, Message: "upgraded curl",
	}, map[string]string{"X-Agent-ID": "a1", "Authorization": "Bearer tok"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, body %s", rec.Code, rec.Body.String())
	}

	results := s.hub.Results("a1")
	if len(results) != 1 || results[0].ActionID != "act1" || !results[0].Success {
		t.Fatalf("unexpected results: %#v", results)
	}

	rec = doJSON(t, s, http.MethodPost, "/companion/result", companionResultRequest{ActionID: "act1"}, map[string]string{
		"X-Agent-ID": "a1", "Authorization": "Bearer wrong",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401 for wrong token", rec.Code)
	}
}

func TestHandleAdminPageShowsCompanionStatus(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	adminBody := func() string {
		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Body.String()
	}

	body := adminBody()
	if !strings.Contains(body, "not connected") || strings.Contains(body, "Upgrade all") {
		t.Fatalf("expected no apply UI before a companion connects, got: %s", body)
	}

	res := s.hub.Connect("a1", KindCompanion, "v0.0.0-test")
	defer s.hub.Disconnect("a1", res.Ch)

	body = adminBody()
	if strings.Contains(body, "not connected") || !strings.Contains(body, "Upgrade all") {
		t.Fatalf("expected apply UI once a companion is connected, got: %s", body)
	}

	s.hub.RecordResult("a1", ActionResult{ActionID: "act1", Success: true, Message: "upgraded curl", CompletedAt: time.Now()})
	body = adminBody()
	if !strings.Contains(body, "recent actions") || !strings.Contains(body, "upgraded curl") {
		t.Fatalf("expected recent action result shown, got: %s", body)
	}
}

// TestHandleAdminPageAgentOnlyShowsRecheckButNotApply covers the
// agent-only connection case: Force-recheck must be available (recheck
// works with either kind), but apply-only controls (Upgrade all, Full
// upgrade all, per-package apply) must not be, since only a real
// companion can run apt-get.
func TestHandleAdminPageAgentOnlyShowsRecheckButNotApply(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	res := s.hub.Connect("a1", KindAgent, "")
	defer s.hub.Disconnect("a1", res.Ch)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()

	if strings.Contains(body, "not connected") {
		t.Fatalf("expected a connected label for an agent-only stream, got: %s", body)
	}
	if !strings.Contains(body, "Force recheck") {
		t.Fatalf("expected Force recheck to be available for an agent-only stream, got: %s", body)
	}
	if strings.Contains(body, "Upgrade all") || strings.Contains(body, "Full upgrade all") {
		t.Fatalf("expected no apply UI for an agent-only stream (no companion), got: %s", body)
	}
	if !strings.Contains(body, "install companion to enable apply") {
		t.Fatalf("expected a hint to install the companion, got: %s", body)
	}
}

func TestHandleAdminPageShowsAggregatorUpdateBanner(t *testing.T) {
	withVersion(t, "v1.0.0")
	s, _ := newTestServerWithLatestVersion(t, "", "v2.0.0")

	rec := doJSON(t, s, http.MethodGet, "/admin", nil, nil)
	body := rec.Body.String()
	if !strings.Contains(body, "v2.0.0") || !strings.Contains(body, "is available") {
		t.Fatalf("expected an update-available banner mentioning v2.0.0, got: %s", body)
	}
}

func TestHandleAdminPageNoBannerWhenAggregatorUpToDate(t *testing.T) {
	withVersion(t, "v1.0.0")
	s, _ := newTestServerWithLatestVersion(t, "", "v1.0.0")

	rec := doJSON(t, s, http.MethodGet, "/admin", nil, nil)
	body := rec.Body.String()
	if strings.Contains(body, "is available") {
		t.Fatalf("expected no update banner when already on the latest version, got: %s", body)
	}
}

func TestHandleAdminPageShowsSelfUpdateButtonsWhenCompanionConnectedAndBehind(t *testing.T) {
	// Aggregator itself is up to date (matches latest) -- only the
	// agent/companion are behind, so their own buttons are the ones
	// expected to show.
	withVersion(t, "v2.0.0")
	s, reg := newTestServerWithLatestVersion(t, "", "v2.0.0")
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	rec := doJSON(t, s, http.MethodPost, "/report", checker.Status{Hostname: "web01", OK: true, AgentVersion: "v0.9.0"}, map[string]string{
		"X-Agent-ID": "a1", "Authorization": "Bearer tok",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("report failed: %d %s", rec.Code, rec.Body.String())
	}

	res := s.hub.Connect("a1", KindCompanion, "v0.9.0")
	defer s.hub.Disconnect("a1", res.Ch)

	// Check for the actual button markup (postSelfUpdate('a1', <component>),
	// not the loose text "Update agent"/etc, which also appears in the
	// fleet-wide banner's own descriptive copy above and would otherwise
	// false-match regardless of whether any button was actually rendered.
	body := doJSON(t, s, http.MethodGet, "/admin", nil, nil).Body.String()
	if !strings.Contains(body, "postSelfUpdate('a1', 'agent'") {
		t.Fatalf("expected an Update agent button, got: %s", body)
	}
	if !strings.Contains(body, "postSelfUpdate('a1', 'companion'") {
		t.Fatalf("expected an Update companion button, got: %s", body)
	}
	if strings.Contains(body, "postSelfUpdate('a1', 'aggregator'") {
		t.Fatalf("expected no Update aggregator button -- it's already up to date, got: %s", body)
	}
}

// TestHandleAdminPageHidesAgentCompanionButtonsWhenAggregatorItselfBehind
// is the regression test for a real UX bug: handleAdminSelfUpdate already
// rejects (409) any agent/companion target newer than the aggregator's
// own running version, but the buttons themselves didn't reflect that --
// an operator could click "Update agent" while the aggregator was behind
// and just get a rejection. Only "Update aggregator" should be offered
// until the aggregator itself catches up.
func TestHandleAdminPageHidesAgentCompanionButtonsWhenAggregatorItselfBehind(t *testing.T) {
	withVersion(t, "v1.0.0")
	s, reg := newTestServerWithLatestVersion(t, "", "v2.0.0")
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	rec := doJSON(t, s, http.MethodPost, "/report", checker.Status{Hostname: "web01", OK: true, AgentVersion: "v0.9.0"}, map[string]string{
		"X-Agent-ID": "a1", "Authorization": "Bearer tok",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("report failed: %d %s", rec.Code, rec.Body.String())
	}

	res := s.hub.Connect("a1", KindCompanion, "v0.9.0")
	defer s.hub.Disconnect("a1", res.Ch)
	s.hub.SetAggregatorPresent("a1", true)

	body := doJSON(t, s, http.MethodGet, "/admin", nil, nil).Body.String()
	if strings.Contains(body, "postSelfUpdate('a1', 'agent'") {
		t.Fatalf("expected no Update agent button while the aggregator itself is behind, got: %s", body)
	}
	if strings.Contains(body, "postSelfUpdate('a1', 'companion'") {
		t.Fatalf("expected no Update companion button while the aggregator itself is behind, got: %s", body)
	}
	if !strings.Contains(body, "postSelfUpdate('a1', 'aggregator'") {
		t.Fatalf("expected an Update aggregator button, got: %s", body)
	}
}

// TestHandleAdminPageHidesUpdateAggregatorButtonWhenNotColocated is the
// regression test for the companion piece of the same class of UX bug:
// an update being available and a companion being connected used to be
// the only two conditions gating the "Update aggregator" button, so it
// showed up on every such row even when that host doesn't run the
// aggregator at all -- clicking it there just fails immediately (see
// SelfUpdate's DeployNone fallthrough). The button must not render
// unless this host's own companion has actually reported the aggregator
// as colocated (SetAggregatorPresent).
func TestHandleAdminPageHidesUpdateAggregatorButtonWhenNotColocated(t *testing.T) {
	withVersion(t, "v1.0.0")
	s, reg := newTestServerWithLatestVersion(t, "", "v2.0.0")
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	rec := doJSON(t, s, http.MethodPost, "/report", checker.Status{Hostname: "web01", OK: true, AgentVersion: "v0.9.0"}, map[string]string{
		"X-Agent-ID": "a1", "Authorization": "Bearer tok",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("report failed: %d %s", rec.Code, rec.Body.String())
	}

	res := s.hub.Connect("a1", KindCompanion, "v0.9.0")
	defer s.hub.Disconnect("a1", res.Ch)
	// Deliberately not calling SetAggregatorPresent -- default false,
	// same as a companion binary that predates this signal entirely.

	body := doJSON(t, s, http.MethodGet, "/admin", nil, nil).Body.String()
	if strings.Contains(body, "postSelfUpdate('a1', 'aggregator'") {
		t.Fatalf("expected no Update aggregator button when this host's companion never reported the aggregator as colocated, got: %s", body)
	}
}

func TestHandleAdminPageNoSelfUpdateButtonsWhenUpToDate(t *testing.T) {
	withVersion(t, "v1.0.0")
	s, reg := newTestServerWithLatestVersion(t, "", "v1.0.0")
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	rec := doJSON(t, s, http.MethodPost, "/report", checker.Status{Hostname: "web01", OK: true, AgentVersion: "v1.0.0"}, map[string]string{
		"X-Agent-ID": "a1", "Authorization": "Bearer tok",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("report failed: %d %s", rec.Code, rec.Body.String())
	}
	res := s.hub.Connect("a1", KindCompanion, "v1.0.0")
	defer s.hub.Disconnect("a1", res.Ch)

	body := doJSON(t, s, http.MethodGet, "/admin", nil, nil).Body.String()
	if strings.Contains(body, `onclick="postSelfUpdate(`) {
		t.Fatalf("expected no self-update buttons when everything is already up to date, got: %s", body)
	}
}

func TestHandleAdminPageNoSelfUpdateButtonsForAgentOnlyStream(t *testing.T) {
	withVersion(t, "v1.0.0")
	s, reg := newTestServerWithLatestVersion(t, "", "v2.0.0")
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	rec := doJSON(t, s, http.MethodPost, "/report", checker.Status{Hostname: "web01", OK: true, AgentVersion: "v0.9.0"}, map[string]string{
		"X-Agent-ID": "a1", "Authorization": "Bearer tok",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("report failed: %d %s", rec.Code, rec.Body.String())
	}
	res := s.hub.Connect("a1", KindAgent, "")
	defer s.hub.Disconnect("a1", res.Ch)

	body := doJSON(t, s, http.MethodGet, "/admin", nil, nil).Body.String()
	if strings.Contains(body, `onclick="postSelfUpdate(`) {
		t.Fatalf("expected no self-update buttons for an agent-only stream (self-update always requires a real companion), got: %s", body)
	}
}

func TestHandleAdminApplyDisabledWithoutSecret(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	rec := doJSON(t, s, http.MethodPost, "/admin/agents/a1/apply", applyRequest{Type: ActionUpgrade}, nil)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("got status %d, want 501 when ADMIN_APPLY_SHARED_SECRET is unset", rec.Code)
	}
}

func TestHandleAdminApplyRequiresSecret(t *testing.T) {
	s, reg := newTestServerWithSecret(t, "s3cret")
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	rec := doJSON(t, s, http.MethodPost, "/admin/agents/a1/apply", applyRequest{Type: ActionUpgrade}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got status %d, want 403 with no secret header", rec.Code)
	}

	rec = doJSON(t, s, http.MethodPost, "/admin/agents/a1/apply", applyRequest{Type: ActionUpgrade}, map[string]string{
		"X-Admin-Apply-Secret": "wrong",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got status %d, want 403 with wrong secret", rec.Code)
	}
}

func TestHandleAdminApplyValidatesRequest(t *testing.T) {
	s, reg := newTestServerWithSecret(t, "s3cret")
	approvedAgent(t, s, reg, "a1", "web01", "tok")
	headers := map[string]string{"X-Admin-Apply-Secret": "s3cret"}

	rec := doJSON(t, s, http.MethodPost, "/admin/agents/a1/apply", applyRequest{Type: "bogus"}, headers)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 for invalid type", rec.Code)
	}

	rec = doJSON(t, s, http.MethodPost, "/admin/agents/a1/apply", applyRequest{Type: ActionPackages}, headers)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 for type=packages with no packages", rec.Code)
	}

	rec = doJSON(t, s, http.MethodPost, "/admin/agents/unknown/apply", applyRequest{Type: ActionUpgrade}, headers)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404 for unknown agent", rec.Code)
	}

	// recheck has its own no-secret endpoint specifically so it doesn't
	// need to go through this one -- /apply must not accept it as a
	// backdoor around that.
	rec = doJSON(t, s, http.MethodPost, "/admin/agents/a1/apply", applyRequest{Type: ActionRecheck}, headers)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 for type=recheck via /apply", rec.Code)
	}
}

func TestHandleAdminApplyRequiresConnectedCompanion(t *testing.T) {
	s, reg := newTestServerWithSecret(t, "s3cret")
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	rec := doJSON(t, s, http.MethodPost, "/admin/agents/a1/apply", applyRequest{Type: ActionUpgrade}, map[string]string{
		"X-Admin-Apply-Secret": "s3cret",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("got status %d, want 409 with no companion connected", rec.Code)
	}
}

func TestHandleAdminApplyPushesToConnectedCompanion(t *testing.T) {
	s, reg := newTestServerWithSecret(t, "s3cret")
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	res := s.hub.Connect("a1", KindCompanion, "v0.0.0-test")
	defer s.hub.Disconnect("a1", res.Ch)

	rec := doJSON(t, s, http.MethodPost, "/admin/agents/a1/apply", applyRequest{
		Type: ActionPackages, Packages: []string{"curl"},
	}, map[string]string{"X-Admin-Apply-Secret": "s3cret"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got status %d, body %s", rec.Code, rec.Body.String())
	}

	select {
	case action := <-res.Ch:
		if action.Type != ActionPackages || len(action.Packages) != 1 || action.Packages[0] != "curl" {
			t.Fatalf("unexpected action pushed: %#v", action)
		}
	default:
		t.Fatal("expected an action to be pushed to the connected channel")
	}
}

func TestHandleAdminRecheckNeedsNoSecret(t *testing.T) {
	// No secret configured at all, unlike apply -- recheck must still work.
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	res := s.hub.Connect("a1", KindCompanion, "v0.0.0-test")
	defer s.hub.Disconnect("a1", res.Ch)

	rec := doJSON(t, s, http.MethodPost, "/admin/agents/a1/recheck", nil, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got status %d, body %s", rec.Code, rec.Body.String())
	}

	select {
	case action := <-res.Ch:
		if action.Type != ActionRecheck {
			t.Fatalf("got action type %q, want recheck", action.Type)
		}
	default:
		t.Fatal("expected a recheck action to be pushed to the connected channel")
	}
}

// TestHandleAdminRecheckWithNoCompanionStillResolvesLiveViewAsDone is the
// regression test for extending live output to Force Recheck: when no
// companion is connected (a bare agent-only KindAgent stream), a recheck
// action never opens POST /companion/output at all -- Begin must still
// happen at push time (inside handleAdminRecheck itself) so a browser
// subscriber's live view still receives "done" once the agent's own
// result lands (via cmd/update-detector/main.go's own report call),
// instead of waiting forever for an End that would otherwise never come.
func TestHandleAdminRecheckWithNoCompanionStillResolvesLiveViewAsDone(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")
	res := s.hub.Connect("a1", KindAgent, "")
	defer s.hub.Disconnect("a1", res.Ch)

	rec := doJSON(t, s, http.MethodPost, "/admin/agents/a1/recheck", nil, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got status %d, body %s", rec.Code, rec.Body.String())
	}
	var accepted struct {
		ActionID string `json:"action_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}

	ch, cancel := s.outputHub.Subscribe("a1")
	defer cancel()

	resultRec := doJSON(t, s, http.MethodPost, "/companion/result",
		companionResultRequest{ActionID: accepted.ActionID, Success: true, Message: "recheck triggered"},
		map[string]string{"X-Agent-ID": "a1", "Authorization": "Bearer tok"})
	if resultRec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", resultRec.Code)
	}

	select {
	case ev := <-ch:
		if ev.Kind != EventDone {
			t.Fatalf("got %#v, want EventDone", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the done event -- Begin must happen at push time, not just inside handleCompanionOutput")
	}
}

func TestHandleAdminRecheckRequiresConnectedCompanion(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	rec := doJSON(t, s, http.MethodPost, "/admin/agents/a1/recheck", nil, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got status %d, want 409 with no companion connected", rec.Code)
	}
}

// withVersion temporarily overrides the package-level version.Version
// for the duration of a test -- handleAdminSelfUpdate's dependency-
// ordering check compares a request's target against this aggregator's
// own currently-running version, which defaults to the unparseable "dev"
// placeholder under `go test` (no -ldflags), so most of these tests need
// a real vX.Y.Z value to exercise that comparison at all.
func withVersion(t *testing.T, v string) {
	t.Helper()
	orig := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = orig })
}

func TestHandleAdminSelfUpdateRequiresSecret(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	rec := doJSON(t, s, http.MethodPost, "/admin/agents/a1/self-update", selfUpdateRequest{Component: "agent", TargetVersion: "v1.0.0"}, nil)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("got status %d, want 501 when ADMIN_APPLY_SHARED_SECRET is unset", rec.Code)
	}

	s2, reg2 := newTestServerWithSecret(t, "s3cret")
	approvedAgent(t, s2, reg2, "a1", "web01", "tok")
	rec = doJSON(t, s2, http.MethodPost, "/admin/agents/a1/self-update", selfUpdateRequest{Component: "agent", TargetVersion: "v1.0.0"}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got status %d, want 403 with no secret header", rec.Code)
	}
}

func TestHandleAdminSelfUpdateValidatesRequest(t *testing.T) {
	s, reg := newTestServerWithSecret(t, "s3cret")
	approvedAgent(t, s, reg, "a1", "web01", "tok")
	headers := map[string]string{"X-Admin-Apply-Secret": "s3cret"}

	rec := doJSON(t, s, http.MethodPost, "/admin/agents/a1/self-update", selfUpdateRequest{Component: "bogus", TargetVersion: "v1.0.0"}, headers)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 for an invalid component", rec.Code)
	}

	rec = doJSON(t, s, http.MethodPost, "/admin/agents/a1/self-update", selfUpdateRequest{Component: "agent"}, headers)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400 for a missing target_version", rec.Code)
	}

	rec = doJSON(t, s, http.MethodPost, "/admin/agents/unknown/self-update", selfUpdateRequest{Component: "agent", TargetVersion: "v1.0.0"}, headers)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404 for unknown agent", rec.Code)
	}
}

func TestHandleAdminSelfUpdateRejectsAgentTargetNewerThanAggregator(t *testing.T) {
	withVersion(t, "v1.0.0")
	s, reg := newTestServerWithSecret(t, "s3cret")
	approvedAgent(t, s, reg, "a1", "web01", "tok")
	res := s.hub.Connect("a1", KindCompanion, "v0.0.0-test")
	defer s.hub.Disconnect("a1", res.Ch)

	rec := doJSON(t, s, http.MethodPost, "/admin/agents/a1/self-update", selfUpdateRequest{
		Component: "agent", TargetVersion: "v2.0.0",
	}, map[string]string{"X-Admin-Apply-Secret": "s3cret"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("got status %d, body %s, want 409 for a target newer than the aggregator's own version", rec.Code, rec.Body.String())
	}

	select {
	case <-res.Ch:
		t.Fatal("expected nothing to be pushed to the companion for a rejected request")
	default:
	}
}

func TestHandleAdminSelfUpdateAllowsAgentTargetNotNewerThanAggregator(t *testing.T) {
	withVersion(t, "v1.0.0")
	s, reg := newTestServerWithSecret(t, "s3cret")
	approvedAgent(t, s, reg, "a1", "web01", "tok")
	res := s.hub.Connect("a1", KindCompanion, "v0.0.0-test")
	defer s.hub.Disconnect("a1", res.Ch)

	rec := doJSON(t, s, http.MethodPost, "/admin/agents/a1/self-update", selfUpdateRequest{
		Component: "agent", TargetVersion: "v1.0.0",
	}, map[string]string{"X-Admin-Apply-Secret": "s3cret"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got status %d, body %s, want 202 for a target equal to the aggregator's own version", rec.Code, rec.Body.String())
	}

	select {
	case action := <-res.Ch:
		if action.Type != ActionSelfUpdate || action.Component != "agent" || action.TargetVersion != "v1.0.0" {
			t.Fatalf("unexpected action pushed: %#v", action)
		}
	default:
		t.Fatal("expected an action to be pushed to the connected channel")
	}
}

func TestHandleAdminSelfUpdateAggregatorComponentSkipsOrderingCheck(t *testing.T) {
	withVersion(t, "v1.0.0")
	s, reg := newTestServerWithSecret(t, "s3cret")
	approvedAgent(t, s, reg, "a1", "web01", "tok")
	res := s.hub.Connect("a1", KindCompanion, "v0.0.0-test")
	defer s.hub.Disconnect("a1", res.Ch)

	// A target "newer" than the aggregator's own version is exactly what
	// updating the aggregator itself means -- must not be rejected by
	// the same ordering check that applies to agent/companion.
	rec := doJSON(t, s, http.MethodPost, "/admin/agents/a1/self-update", selfUpdateRequest{
		Component: "aggregator", TargetVersion: "v2.0.0",
	}, map[string]string{"X-Admin-Apply-Secret": "s3cret"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got status %d, body %s, want 202 for component=aggregator regardless of target version", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminSelfUpdateRequiresConnectedCompanion(t *testing.T) {
	withVersion(t, "v1.0.0")
	s, reg := newTestServerWithSecret(t, "s3cret")
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	rec := doJSON(t, s, http.MethodPost, "/admin/agents/a1/self-update", selfUpdateRequest{
		Component: "agent", TargetVersion: "v1.0.0",
	}, map[string]string{"X-Admin-Apply-Secret": "s3cret"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("got status %d, want 409 with no companion connected", rec.Code)
	}
}

func TestHandleAdminSelfUpdateRejectsAgentOnlyStream(t *testing.T) {
	withVersion(t, "v1.0.0")
	s, reg := newTestServerWithSecret(t, "s3cret")
	approvedAgent(t, s, reg, "a1", "web01", "tok")
	s.hub.Connect("a1", KindAgent, "")

	rec := doJSON(t, s, http.MethodPost, "/admin/agents/a1/self-update", selfUpdateRequest{
		Component: "agent", TargetVersion: "v1.0.0",
	}, map[string]string{"X-Admin-Apply-Secret": "s3cret"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("got status %d, body %s, want 409 -- self-update always needs a real companion, never an agent-only stream", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminSelfUpdateChannelRequiresConfiguredSelfUpdate(t *testing.T) {
	s, _ := newTestServerWithSecret(t, "s3cret")
	rec := doJSON(t, s, http.MethodPost, "/admin/self-update-channel", selfUpdateChannelRequest{IncludePreRelease: true}, map[string]string{"X-Admin-Apply-Secret": "s3cret"})
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("got status %d, want 501 when the server has no selfupdate.Client at all", rec.Code)
	}
}

func TestHandleAdminSelfUpdateChannelRequiresSecret(t *testing.T) {
	s, _ := newTestServerWithLatestVersion(t, "", "v1.0.0")
	rec := doJSON(t, s, http.MethodPost, "/admin/self-update-channel", selfUpdateChannelRequest{IncludePreRelease: true}, nil)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("got status %d, want 501 when ADMIN_APPLY_SHARED_SECRET is unset", rec.Code)
	}

	s2, _ := newTestServerWithLatestVersion(t, "s3cret", "v1.0.0")
	rec = doJSON(t, s2, http.MethodPost, "/admin/self-update-channel", selfUpdateChannelRequest{IncludePreRelease: true}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got status %d, want 403 with no secret header", rec.Code)
	}
}

func TestHandleAdminSelfUpdateChannelSwitchesAndRefreshesImmediately(t *testing.T) {
	s, _ := newTestServerWithLatestVersion(t, "s3cret", "v1.0.0")
	if s.selfUpdate.IncludePreRelease() {
		t.Fatal("expected the fake client to start on the release-only channel")
	}

	rec := doJSON(t, s, http.MethodPost, "/admin/self-update-channel", selfUpdateChannelRequest{IncludePreRelease: true}, map[string]string{"X-Admin-Apply-Secret": "s3cret"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, body %s, want 200", rec.Code, rec.Body.String())
	}
	if !s.selfUpdate.IncludePreRelease() {
		t.Fatal("expected the channel to actually switch to include-prerelease")
	}
}

func TestHandleHealthz(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/healthz", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Fatalf("got status field %q, want ok", body["status"])
	}
	if _, ok := body["version"]; !ok {
		t.Fatal("expected a version field in the healthz response")
	}
}

func TestHandleAdminRecheckUnknownAgent(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPost, "/admin/agents/unknown/recheck", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404 for unknown agent", rec.Code)
	}
}

func TestHandleAdminAgentVersionUnknownAgent(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/admin/agents/unknown/version", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404 for unknown agent", rec.Code)
	}
}

// TestHandleAdminAgentVersionReflectsLastReportAndCompanionVersion covers
// the fix for the admin page reloading onto a stale agent/companion
// version after a self-update: the poll this endpoint backs must reflect
// the registry's/hub's own current state directly, not some snapshot
// taken at a page render that predates the update actually landing.
func TestHandleAdminAgentVersionReflectsLastReportAndCompanionVersion(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	rec := doJSON(t, s, http.MethodGet, "/admin/agents/a1/version", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["agent_version"] != "" || resp["companion_version"] != "" || resp["last_seen"] != "" {
		t.Fatalf("got %+v, want all empty before any report/connect", resp)
	}

	if outcome, err := reg.Report("a1", "tok", checker.Status{AgentVersion: "v1.2.3"}); err != nil || outcome != ReportAccepted {
		t.Fatalf("Report: outcome=%v err=%v", outcome, err)
	}
	connectRes := s.hub.Connect("a1", KindCompanion, "v1.2.0")
	defer s.hub.Disconnect("a1", connectRes.Ch)

	rec = doJSON(t, s, http.MethodGet, "/admin/agents/a1/version", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, body %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["agent_version"] != "v1.2.3" {
		t.Fatalf("got agent_version %q, want v1.2.3", resp["agent_version"])
	}
	if resp["companion_version"] != "v1.2.0" {
		t.Fatalf("got companion_version %q, want v1.2.0", resp["companion_version"])
	}
	if resp["last_seen"] == "" {
		t.Fatalf("got empty last_seen, want it set after Report")
	}
}

// TestHandleAdminAgentVersionLastSeenAdvancesOnEachReport covers the
// signal watchReportUpdated (admin page JS) polls on after a plain apply:
// last_seen must change on every fresh report, not just the first one,
// so a baseline captured right before an apply reliably differs once the
// resulting recheck's report actually lands.
func TestHandleAdminAgentVersionLastSeenAdvancesOnEachReport(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	if outcome, err := reg.Report("a1", "tok", checker.Status{}); err != nil || outcome != ReportAccepted {
		t.Fatalf("first Report: outcome=%v err=%v", outcome, err)
	}
	rec := doJSON(t, s, http.MethodGet, "/admin/agents/a1/version", nil, nil)
	var first map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}

	time.Sleep(time.Millisecond) // guards against a low-resolution clock reporting the exact same instant twice
	if outcome, err := reg.Report("a1", "tok", checker.Status{}); err != nil || outcome != ReportAccepted {
		t.Fatalf("second Report: outcome=%v err=%v", outcome, err)
	}
	rec = doJSON(t, s, http.MethodGet, "/admin/agents/a1/version", nil, nil)
	var second map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}

	if first["last_seen"] == "" || second["last_seen"] == "" {
		t.Fatalf("got first=%q second=%q, want both non-empty", first["last_seen"], second["last_seen"])
	}
	if first["last_seen"] == second["last_seen"] {
		t.Fatalf("got the same last_seen %q across two separate reports, want it to advance", first["last_seen"])
	}
}

func TestHandleAdminApplyRejectsWhenActionInFlight(t *testing.T) {
	s, reg := newTestServerWithSecret(t, "s3cret")
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	res := s.hub.Connect("a1", KindCompanion, "v0.0.0-test")
	defer s.hub.Disconnect("a1", res.Ch)
	headers := map[string]string{"X-Admin-Apply-Secret": "s3cret"}

	rec := doJSON(t, s, http.MethodPost, "/admin/agents/a1/apply", applyRequest{Type: ActionUpgrade}, headers)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got status %d, body %s for the first apply", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/admin/agents/a1/apply", applyRequest{Type: ActionUpgrade}, headers)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got status %d, want 409 for a second apply before the first resolves", rec.Code)
	}

	// Draining the first action and reporting its result should unblock
	// a subsequent apply.
	action := <-res.Ch
	s.hub.RecordResult("a1", ActionResult{ActionID: action.ID, Success: true})

	rec = doJSON(t, s, http.MethodPost, "/admin/agents/a1/apply", applyRequest{Type: ActionUpgrade}, headers)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got status %d, body %s for the third apply after the first resolved", rec.Code, rec.Body.String())
	}
}

// TestHandleCompanionResultStagedAutoPushesSwapAction verifies that when a
// companion reports a Staged result (e.g. companion self-update on Windows
// where the companion downloaded .exe.new but can't swap its own running
// binary), the aggregator auto-pushes ActionCompleteCompanionSwap to the
// agent stream on the same host.
func TestHandleCompanionResultStagedAutoPushesSwapAction(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	// Connect an agent stream (not a companion) so we can see actions
	// pushed to the agent.
	agentRes := s.hub.Connect("a1", KindAgent, "v0.0.0-test")
	defer s.hub.Disconnect("a1", agentRes.Ch)

	// Report a staged result from the companion.
	rec := doJSON(t, s, http.MethodPost, "/companion/result", companionResultRequest{
		ActionID: "act1",
		Success:  true,
		Message:  "companion update staged to .exe.new",
		Staged:   true,
	}, map[string]string{"X-Agent-ID": "a1", "Authorization": "Bearer tok"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, body %s", rec.Code, rec.Body.String())
	}

	// The aggregator should have auto-pushed an ActionCompleteCompanionSwap
	// to the agent stream.
	select {
	case action := <-agentRes.Ch:
		if action.Type != ActionCompleteCompanionSwap {
			t.Fatalf("expected ActionCompleteCompanionSwap, got %q", action.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ActionCompleteCompanionSwap to be pushed to agent")
	}
}

// TestHandleCompanionResultNonStagedDoesNotPushSwapAction verifies that
// a normal (non-staged) result does NOT trigger an auto-push of
// ActionCompleteCompanionSwap.
func TestHandleCompanionResultNonStagedDoesNotPushSwapAction(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	agentRes := s.hub.Connect("a1", KindAgent, "v0.0.0-test")
	defer s.hub.Disconnect("a1", agentRes.Ch)

	// Report a normal (non-staged) result.
	rec := doJSON(t, s, http.MethodPost, "/companion/result", companionResultRequest{
		ActionID: "act1",
		Success:  true,
		Message:  "upgraded curl",
	}, map[string]string{"X-Agent-ID": "a1", "Authorization": "Bearer tok"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, body %s", rec.Code, rec.Body.String())
	}

	// No ActionCompleteCompanionSwap should be pushed.
	select {
	case action := <-agentRes.Ch:
		t.Fatalf("unexpected action pushed to agent: %q", action.Type)
	case <-time.After(200 * time.Millisecond):
		// Expected: no action pushed.
	}
}

// TestHandleCompanionResultStagedFailedDoesNotPushSwapAction verifies that
// a staged but failed result does NOT trigger an auto-push — only
// successful staged results should.
func TestHandleCompanionResultStagedFailedDoesNotPushSwapAction(t *testing.T) {
	s, reg := newTestServer(t)
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	agentRes := s.hub.Connect("a1", KindAgent, "v0.0.0-test")
	defer s.hub.Disconnect("a1", agentRes.Ch)

	// Report a staged but failed result.
	rec := doJSON(t, s, http.MethodPost, "/companion/result", companionResultRequest{
		ActionID: "act1",
		Success:  false,
		Message:  "download failed",
		Staged:   true,
	}, map[string]string{"X-Agent-ID": "a1", "Authorization": "Bearer tok"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, body %s", rec.Code, rec.Body.String())
	}

	// No ActionCompleteCompanionSwap should be pushed for a failed result.
	select {
	case action := <-agentRes.Ch:
		t.Fatalf("unexpected action pushed to agent: %q", action.Type)
	case <-time.After(200 * time.Millisecond):
		// Expected: no action pushed.
	}
}
