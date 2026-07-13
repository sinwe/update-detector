package aggregator

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"update-detector/internal/checker"
	"update-detector/internal/notifier"
	"update-detector/openapi"
)

type Server struct {
	registry  *Registry
	hub       *CompanionHub
	notifyMgr *notifier.Manager
	// adminApplySecret gates POST /admin/agents/{id}/apply. Empty means the
	// endpoint is disabled (501) -- this higher-stakes capability is
	// opt-in, unlike the rest of /admin which trusts the network path.
	adminApplySecret string
	mux              *http.ServeMux
}

func NewServer(registry *Registry, hub *CompanionHub, notifyMgr *notifier.Manager, adminApplySecret string) *Server {
	s := &Server{
		registry:         registry,
		hub:              hub,
		notifyMgr:        notifyMgr,
		adminApplySecret: adminApplySecret,
		mux:              http.NewServeMux(),
	}
	s.mux.HandleFunc("/", s.handleRoot)
	s.mux.HandleFunc("/enroll", s.handleEnroll)
	s.mux.HandleFunc("/report", s.handleReport)
	s.mux.HandleFunc("/companion/stream", s.handleCompanionStream)
	s.mux.HandleFunc("/companion/result", s.handleCompanionResult)
	s.mux.HandleFunc("/admin", s.handleAdmin)
	s.mux.HandleFunc("/admin/agents/", s.handleAdminAction)
	s.mux.HandleFunc("/widgets/summary", s.handleWidgetSummary)
	s.mux.HandleFunc("/widgets/hosts", s.handleWidgetHosts)
	s.mux.HandleFunc("/widgets/hosts/", s.handleWidgetHost)
	s.mux.HandleFunc("/widgets/packages", s.handleWidgetPackages)
	s.mux.HandleFunc("/openapi.yaml", s.handleOpenAPISpec)
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

type enrollRequest struct {
	AgentID  string `json:"agent_id"`
	Hostname string `json:"hostname"`
	Token    string `json:"token"`
}

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req enrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.AgentID == "" || req.Hostname == "" || req.Token == "" {
		http.Error(w, "agent_id, hostname, and token are required", http.StatusBadRequest)
		return
	}

	outcome, status, err := s.registry.Enroll(req.AgentID, req.Hostname, req.Token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if outcome == EnrollConflict {
		http.Error(w, "agent_id already registered with a different token", http.StatusConflict)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": string(status)})
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agentID := r.Header.Get("X-Agent-ID")
	token := bearerToken(r)
	if agentID == "" || token == "" {
		http.Error(w, "missing X-Agent-ID or Authorization: Bearer token", http.StatusUnauthorized)
		return
	}

	var status checker.Status
	if err := json.NewDecoder(r.Body).Decode(&status); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	outcome, err := s.registry.Report(agentID, token, status)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	switch outcome {
	case ReportUnknownAgent:
		http.Error(w, "unknown agent", http.StatusUnauthorized)
	case ReportUnauthorized:
		http.Error(w, "invalid token", http.StatusUnauthorized)
	case ReportNotApproved:
		http.Error(w, "agent not approved", http.StatusForbidden)
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
	}
}

// authenticateCompanion applies the same X-Agent-ID/Bearer-token trust
// check /report uses to a companion transport endpoint (stream or result),
// writing an appropriate error response and returning ok=false if it
// fails.
func (s *Server) authenticateCompanion(w http.ResponseWriter, r *http.Request) (AgentRecord, bool) {
	agentID := r.Header.Get("X-Agent-ID")
	token := bearerToken(r)
	if agentID == "" || token == "" {
		http.Error(w, "missing X-Agent-ID or Authorization: Bearer token", http.StatusUnauthorized)
		return AgentRecord{}, false
	}
	rec, outcome := s.registry.Authenticate(agentID, token)
	switch outcome {
	case AuthUnknownAgent, AuthUnauthorized:
		http.Error(w, "invalid agent_id or token", http.StatusUnauthorized)
		return AgentRecord{}, false
	case AuthNotApproved:
		http.Error(w, "agent not approved", http.StatusForbidden)
		return AgentRecord{}, false
	}
	return rec, true
}

// handleCompanionStream is the SSE endpoint a host-native companion holds
// open long-term: one Action at a time, pushed down whenever an admin
// triggers an apply for this agent. Plain HTTP/1.1, not HTTP/2, to avoid
// TLS/ALPN complexity for what's meant to run over a private network.
func (s *Server) handleCompanionStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rec, ok := s.authenticateCompanion(w, r)
	if !ok {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := s.hub.Connect(rec.ID)
	defer s.hub.Disconnect(rec.ID, ch)

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case action := <-ch:
			payload, err := json.Marshal(action)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

type companionResultRequest struct {
	ActionID string `json:"action_id"`
	Success  bool   `json:"success"`
	Message  string `json:"message,omitempty"`
}

// handleCompanionResult records the outcome of a previously pushed Action
// and, if a notifier is configured, alerts on it (e.g. "update applied on
// web01").
func (s *Server) handleCompanionResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rec, ok := s.authenticateCompanion(w, r)
	if !ok {
		return
	}

	var req companionResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ActionID == "" {
		http.Error(w, "action_id is required", http.StatusBadRequest)
		return
	}

	s.hub.RecordResult(rec.ID, ActionResult{
		ActionID:    req.ActionID,
		Success:     req.Success,
		Message:     req.Message,
		CompletedAt: time.Now(),
	})

	if s.notifyMgr != nil {
		outcome := "failed"
		if req.Success {
			outcome = "applied"
		}
		msg := fmt.Sprintf("update %s on %s", outcome, rec.Hostname)
		if req.Message != "" {
			msg += ": " + req.Message
		}
		status := checker.Status{Hostname: rec.Hostname}
		if rec.LastReport != nil {
			status = *rec.LastReport
		}
		s.notifyMgr.Send(r.Context(), notifier.Event{
			Hostname: rec.Hostname,
			Status:   status,
			Changes:  []string{msg},
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data := adminPageData{}
	for _, rec := range s.registry.List() {
		v := toAgentView(rec, s.hub)
		switch rec.Status {
		case StatusPending:
			data.Pending = append(data.Pending, v)
		case StatusApproved:
			data.Approved = append(data.Approved, v)
		case StatusRejected:
			data.Rejected = append(data.Rejected, v)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := renderAdminPage(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleAdminAction serves POST /admin/agents/{id}/approve and .../reject.
// Revoking an approved agent reuses reject — there's no separate state for it.
func (s *Server) handleAdminAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	trimmed := strings.TrimPrefix(r.URL.Path, "/admin/agents/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	id, action := parts[0], parts[1]

	if action == "apply" {
		s.handleAdminApply(w, r, id)
		return
	}
	if action == "recheck" {
		s.handleAdminRecheck(w, r, id)
		return
	}

	var status Status
	switch action {
	case "approve":
		status = StatusApproved
	case "reject":
		status = StatusRejected
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}

	if err := s.registry.SetStatus(id, status); err != nil {
		if err == ErrNotFound {
			http.Error(w, "agent not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

type applyRequest struct {
	Type     ActionType `json:"type"`
	Packages []string   `json:"packages,omitempty"`
}

// handleAdminApply is the human-triggered counterpart to the companion's
// SSE stream: it pushes an upgrade Action down to a connected companion.
// Fails closed (501) until ADMIN_APPLY_SHARED_SECRET is configured, so this
// higher-stakes capability is opt-in rather than on by default. Works with
// no reverse-proxy setup at all by default -- the admin page's own JS
// prompts for this secret once and remembers it client-side, sending it
// as this same header. If a reverse proxy (e.g. Authentik) fronts this
// endpoint instead and injects the header itself, that works too; either
// way the secret is checked here independently, so a compromised proxy or
// network path alone must not be enough to trigger an apply.
func (s *Server) handleAdminApply(w http.ResponseWriter, r *http.Request, id string) {
	if s.adminApplySecret == "" {
		http.Error(w, "apply is disabled: ADMIN_APPLY_SHARED_SECRET is not configured", http.StatusNotImplemented)
		return
	}
	presented := r.Header.Get("X-Admin-Apply-Secret")
	if presented == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(s.adminApplySecret)) != 1 {
		http.Error(w, "invalid or missing X-Admin-Apply-Secret", http.StatusForbidden)
		return
	}

	rec, ok := s.registry.Get(id)
	if !ok || rec.Status != StatusApproved {
		http.Error(w, "agent not found or not approved", http.StatusNotFound)
		return
	}

	var req applyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	// Deliberately not req.Type.valid() -- that also accepts ActionRecheck,
	// which has its own no-secret-required endpoint (handleAdminRecheck)
	// specifically so it doesn't need to go through this secret-gated one.
	switch req.Type {
	case ActionPackages, ActionUpgrade, ActionFullUpgrade:
	default:
		http.Error(w, "type must be one of: packages, upgrade, full-upgrade", http.StatusBadRequest)
		return
	}
	if req.Type == ActionPackages && len(req.Packages) == 0 {
		http.Error(w, "packages must be non-empty for type=packages", http.StatusBadRequest)
		return
	}

	action := Action{
		ID:        newActionID(),
		Type:      req.Type,
		Packages:  req.Packages,
		CreatedAt: time.Now(),
	}
	if err := s.hub.Push(id, action); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"action_id": action.ID})
}

// handleAdminRecheck pushes a recheck Action to a connected companion, so
// the agent runs an out-of-band detection cycle instead of waiting for the
// next CHECK_INTERVAL -- e.g. after the admin page's data still looks
// stale a while after a manual apply. Unlike handleAdminApply, this needs
// no shared secret: it can't change anything on the host, only make it
// report what's already true sooner, so it gets the same trust model as
// the rest of /admin (approve/reject/view).
func (s *Server) handleAdminRecheck(w http.ResponseWriter, _ *http.Request, id string) {
	rec, ok := s.registry.Get(id)
	if !ok || rec.Status != StatusApproved {
		http.Error(w, "agent not found or not approved", http.StatusNotFound)
		return
	}

	action := Action{ID: newActionID(), Type: ActionRecheck, CreatedAt: time.Now()}
	if err := s.hub.Push(id, action); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"action_id": action.ID})
}

type summaryResponse struct {
	HostsTotal                 int       `json:"hosts_total"`
	HostsReporting             int       `json:"hosts_reporting"`
	HostsOK                    int       `json:"hosts_ok"`
	HostsRebootRequired        int       `json:"hosts_reboot_required"`
	HostsOSUpgradeAvailable    int       `json:"hosts_os_upgrade_available"`
	PackagesUpgradableTotal    int       `json:"packages_upgradable_total"`
	PackagesUpgradableSecurity int       `json:"packages_upgradable_security"`
	GeneratedAt                time.Time `json:"generated_at"`
}

func (s *Server) handleWidgetSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp := summaryResponse{GeneratedAt: time.Now()}
	for _, rec := range s.registry.List() {
		if rec.Status != StatusApproved {
			continue
		}
		resp.HostsTotal++
		if rec.LastReport == nil {
			continue
		}
		resp.HostsReporting++
		if rec.LastReport.OK {
			resp.HostsOK++
		}
		if rec.LastReport.RebootRequired {
			resp.HostsRebootRequired++
		}
		if rec.LastReport.OS.UpdateAvailable {
			resp.HostsOSUpgradeAvailable++
		}
		resp.PackagesUpgradableTotal += rec.LastReport.Packages.UpgradableTotal
		resp.PackagesUpgradableSecurity += rec.LastReport.Packages.UpgradableSecurity
	}
	writeJSON(w, http.StatusOK, resp)
}

type hostSummary struct {
	Hostname           string     `json:"hostname"`
	AgentID            string     `json:"agent_id"`
	HasReport          bool       `json:"has_report"`
	OK                 bool       `json:"ok,omitempty"`
	LastSeen           *time.Time `json:"last_seen,omitempty"`
	UpgradableTotal    int        `json:"upgradable_total,omitempty"`
	UpgradableSecurity int        `json:"upgradable_security,omitempty"`
	RebootRequired     bool       `json:"reboot_required,omitempty"`
	OSUpdateAvailable  bool       `json:"os_update_available,omitempty"`
}

func (s *Server) handleWidgetHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out := []hostSummary{}
	for _, rec := range s.registry.List() {
		if rec.Status != StatusApproved {
			continue
		}
		hs := hostSummary{Hostname: rec.Hostname, AgentID: rec.ID}
		if !rec.LastSeen.IsZero() {
			t := rec.LastSeen
			hs.LastSeen = &t
		}
		if rec.LastReport != nil {
			hs.HasReport = true
			hs.OK = rec.LastReport.OK
			hs.UpgradableTotal = rec.LastReport.Packages.UpgradableTotal
			hs.UpgradableSecurity = rec.LastReport.Packages.UpgradableSecurity
			hs.RebootRequired = rec.LastReport.RebootRequired
			hs.OSUpdateAvailable = rec.LastReport.OS.UpdateAvailable
		}
		out = append(out, hs)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleWidgetHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	hostname := strings.TrimPrefix(r.URL.Path, "/widgets/hosts/")
	if hostname == "" {
		http.Error(w, "hostname required", http.StatusBadRequest)
		return
	}
	rec, ok := s.registry.FindApprovedByHostname(hostname)
	if !ok || rec.LastReport == nil {
		http.Error(w, `{"error":"no report for this host yet"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, rec.LastReport)
}

type pendingPackage struct {
	Hostname         string `json:"hostname"`
	Name             string `json:"name"`
	CurrentVersion   string `json:"current_version,omitempty"`
	CandidateVersion string `json:"candidate_version"`
	Security         bool   `json:"security,omitempty"`
}

// handleWidgetPackages flattens every approved, reporting agent's pending
// package upgrades into one fleet-wide list, so a single Homepage widget
// (or any other consumer) can show "what needs updating" across every host
// without querying each one individually. ?security=true filters to only
// packages flagged as security updates.
func (s *Server) handleWidgetPackages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	securityOnly := r.URL.Query().Get("security") == "true"

	out := []pendingPackage{}
	for _, rec := range s.registry.List() {
		if rec.Status != StatusApproved || rec.LastReport == nil {
			continue
		}
		for _, u := range rec.LastReport.Packages.Upgrades {
			if securityOnly && !u.Security {
				continue
			}
			out = append(out, pendingPackage{
				Hostname:         rec.Hostname,
				Name:             u.Name,
				CurrentVersion:   u.CurrentVersion,
				CandidateVersion: u.CandidateVersion,
				Security:         u.Security,
			})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleOpenAPISpec serves the OpenAPI 3.0 spec for this API (openapi/update-aggregator.yaml).
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openapi.UpdateAggregatorSpec)
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimPrefix(h, prefix)
}

func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(v)
}
