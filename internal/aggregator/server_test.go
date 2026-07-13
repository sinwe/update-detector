package aggregator

import (
	"bytes"
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
)

func newTestServer(t *testing.T) (*Server, *Registry) {
	return newTestServerWithSecret(t, "")
}

func newTestServerWithSecret(t *testing.T, adminApplySecret string) (*Server, *Registry) {
	reg := NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	hub := NewCompanionHub()
	return NewServer(reg, hub, notifier.NewManager(), adminApplySecret), reg
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

	ch := s.hub.Connect("a1")
	defer s.hub.Disconnect("a1", ch)

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

	ch := s.hub.Connect("a1")
	defer s.hub.Disconnect("a1", ch)

	rec := doJSON(t, s, http.MethodPost, "/admin/agents/a1/apply", applyRequest{
		Type: ActionPackages, Packages: []string{"curl"},
	}, map[string]string{"X-Admin-Apply-Secret": "s3cret"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got status %d, body %s", rec.Code, rec.Body.String())
	}

	select {
	case action := <-ch:
		if action.Type != ActionPackages || len(action.Packages) != 1 || action.Packages[0] != "curl" {
			t.Fatalf("unexpected action pushed: %#v", action)
		}
	default:
		t.Fatal("expected an action to be pushed to the connected channel")
	}
}

func TestHandleAdminApplyRejectsWhenActionInFlight(t *testing.T) {
	s, reg := newTestServerWithSecret(t, "s3cret")
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	ch := s.hub.Connect("a1")
	defer s.hub.Disconnect("a1", ch)
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
	action := <-ch
	s.hub.RecordResult("a1", ActionResult{ActionID: action.ID, Success: true})

	rec = doJSON(t, s, http.MethodPost, "/admin/agents/a1/apply", applyRequest{Type: ActionUpgrade}, headers)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got status %d, body %s for the third apply after the first resolved", rec.Code, rec.Body.String())
	}
}
