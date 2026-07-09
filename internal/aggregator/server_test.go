package aggregator

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"update-detector/internal/checker"
)

func newTestServer(t *testing.T) (*Server, *Registry) {
	reg := NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	return NewServer(reg), reg
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
