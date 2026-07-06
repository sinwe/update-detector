package aggregator

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"update-detector/internal/checker"
	"update-detector/openapi"
)

type Server struct {
	registry *Registry
	mux      *http.ServeMux
}

func NewServer(registry *Registry) *Server {
	s := &Server{registry: registry, mux: http.NewServeMux()}
	s.mux.HandleFunc("/", s.handleRoot)
	s.mux.HandleFunc("/enroll", s.handleEnroll)
	s.mux.HandleFunc("/report", s.handleReport)
	s.mux.HandleFunc("/admin", s.handleAdmin)
	s.mux.HandleFunc("/admin/agents/", s.handleAdminAction)
	s.mux.HandleFunc("/widgets/summary", s.handleWidgetSummary)
	s.mux.HandleFunc("/widgets/hosts", s.handleWidgetHosts)
	s.mux.HandleFunc("/widgets/hosts/", s.handleWidgetHost)
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

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data := adminPageData{}
	for _, rec := range s.registry.List() {
		v := toAgentView(rec)
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
