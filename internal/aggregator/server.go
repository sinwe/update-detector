package aggregator

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"update-detector/internal/checker"
	"update-detector/internal/notifier"
	"update-detector/internal/selfupdate"
	"update-detector/internal/version"
	"update-detector/openapi"
)

type Server struct {
	// shutdownCtx is the process's own top-level lifetime context (see
	// cmd/update-aggregator/main.go's run) -- deliberately not just
	// relying on each request's own r.Context(), which is *not*
	// cancelled by http.Server.Shutdown() alone (a well-known net/http
	// gotcha: Shutdown waits for in-flight handlers to return on their
	// own, it doesn't signal them to). Without this, every open
	// /companion/stream connection (agent or companion) sits blocked in
	// its select loop through Shutdown's own grace period and only
	// actually gets closed once the old process calls os.Exit -- meaning
	// a plain restart (e.g. self-update) leaves every connected
	// agent/companion waiting several extra, unnecessary seconds before
	// they even notice the disconnect and start reconnecting. Confirmed
	// as a real (if bounded) slow-reconnect symptom in practice.
	shutdownCtx context.Context
	registry    *Registry
	hub         *CompanionHub
	notifyMgr   *notifier.Manager
	// adminApplySecret gates POST /admin/agents/{id}/apply. Empty means the
	// endpoint is disabled (501) -- this higher-stakes capability is
	// opt-in, unlike the rest of /admin which trusts the network path.
	adminApplySecret string
	// selfUpdate is nil when self-update version checking is disabled
	// (or, in tests, simply not exercised) -- every read of it must be
	// nil-checked, never assumed non-nil.
	selfUpdate *selfupdate.Client
	// outputHub fans a companion's live action output out to browser
	// subscribers -- never nil (unlike selfUpdate, every server has one).
	outputHub *OutputHub
	mux       *http.ServeMux
}

func NewServer(shutdownCtx context.Context, registry *Registry, hub *CompanionHub, notifyMgr *notifier.Manager, adminApplySecret string, selfUpdate *selfupdate.Client, outputHub *OutputHub) *Server {
	s := &Server{
		shutdownCtx:      shutdownCtx,
		registry:         registry,
		hub:              hub,
		notifyMgr:        notifyMgr,
		adminApplySecret: adminApplySecret,
		selfUpdate:       selfUpdate,
		outputHub:        outputHub,
		mux:              http.NewServeMux(),
	}
	s.mux.HandleFunc("/", s.handleRoot)
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/enroll", s.handleEnroll)
	s.mux.HandleFunc("/report", s.handleReport)
	s.mux.HandleFunc("/companion/stream", s.handleCompanionStream)
	s.mux.HandleFunc("/companion/result", s.handleCompanionResult)
	s.mux.HandleFunc("/companion/output", s.handleCompanionOutput)
	s.mux.HandleFunc("/admin", s.handleAdmin)
	s.mux.HandleFunc("/admin/self-update-channel", s.handleAdminSelfUpdateChannel)
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

// handleHealthz mirrors the agent's own GET /healthz
// (internal/httpserver/server.go) -- this is what the admin page's
// Jenkins-style banner polls after triggering a self-update of the
// aggregator itself, to know when it's actually back up and confirm
// which version it's now running (not just "got any response," since a
// stale response during the restart window shouldn't look like success).
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": version.Version})
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

// handleCompanionStream is the SSE endpoint a host-native companion (or,
// when no companion is running, the agent itself) holds open long-term:
// one Action at a time, pushed down whenever an admin triggers an action
// for this agent. Plain HTTP/1.1, not HTTP/2, to avoid TLS/ALPN complexity
// for what's meant to run over a private network.
func (s *Server) handleCompanionStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rec, ok := s.authenticateCompanion(w, r)
	if !ok {
		return
	}

	// hub.Connect must be called -- and its result checked -- before any
	// header is written: a status code can't be un-written once sent, and
	// a rejected connect (companion already holds this agent's slot, and
	// this request is from the lower-priority agent) needs to come back
	// as a plain 409, never an SSE upgrade.
	kind := ParseClientKind(r.Header.Get("X-Client-Kind"))
	result := s.hub.Connect(rec.ID, kind, r.Header.Get("X-Companion-Version"))
	if !result.Accepted {
		http.Error(w, "superseded: a companion is already connected for this agent", http.StatusConflict)
		return
	}
	defer s.hub.Disconnect(rec.ID, result.Ch)
	// Only meaningful for a real companion (see SetAggregatorPresent) --
	// an agent-only connection has never run the aggregator-colocation
	// check at all, so a missing/unparseable header here (including one
	// from an older companion binary that predates this header entirely)
	// defaults to false, same "hide rather than show incorrectly" posture
	// as AggregatorPresent's own zero value.
	if kind == KindCompanion {
		present, _ := strconv.ParseBool(r.Header.Get("X-Aggregator-Present"))
		s.hub.SetAggregatorPresent(rec.ID, present)
		agentPresent, _ := strconv.ParseBool(r.Header.Get("X-Agent-Present"))
		s.hub.SetAgentPresent(rec.ID, agentPresent)
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

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.shutdownCtx.Done():
			// See shutdownCtx's own doc comment: without this, this
			// handler would sit blocked here through the entire process
			// shutdown, needlessly delaying how soon the connected
			// agent/companion notices and starts reconnecting.
			return
		case <-result.Superseded:
			// A companion just connected and preempted this (necessarily
			// agent-kind) stream -- one last distinguishing frame so the
			// client knows not to hot-loop reconnecting, then tear down.
			io.WriteString(w, "event: superseded\ndata: {}\n\n")
			flusher.Flush()
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case action := <-result.Ch:
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
	s.outputHub.End(rec.ID, req.ActionID, EventDone)

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

type companionOutputFrame struct {
	ActionID string `json:"action_id"`
	Line     string `json:"line"`
}

// handleCompanionOutput receives a companion's live action output as it's
// produced: one newline-delimited JSON {"action_id","line"} frame per
// output line, POSTed with a chunked (streaming) request body held open
// for the entire lifetime of that action (see
// internal/companion/outputstream.go). Fans each line out to any browser
// subscribers via outputHub.Publish. However this request body ends --
// cleanly, once the action's own final result already arrived via
// handleCompanionResult, or abruptly, e.g. the companion process was
// killed mid-action while self-updating itself -- outputHub.End's own
// compare-and-clear decides which of those two actually gets reported to
// subscribers; see OutputHub.End.
func (s *Server) handleCompanionOutput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rec, ok := s.authenticateCompanion(w, r)
	if !ok {
		return
	}

	actionID := r.URL.Query().Get("action_id")
	if actionID == "" {
		http.Error(w, "action_id is required", http.StatusBadRequest)
		return
	}
	// Defense in depth, not just a UI nicety: a stream for an action this
	// agent doesn't currently have in flight (stale retry, mismatched ID)
	// must not be allowed to publish anything.
	if pending, ok := s.hub.Pending(rec.ID); !ok || pending != actionID {
		http.Error(w, "action_id does not match this agent's in-flight action", http.StatusConflict)
		return
	}

	// Begin already happened when the action was pushed (handleAdminApply
	// et al.) -- uniformly, whether or not this stream ever actually
	// arrives, so an agent-only recheck (never opens this endpoint at
	// all) and an old companion that predates output streaming entirely
	// both still resolve to a correct "done" via handleCompanionResult
	// instead of a live pane that never closes.
	defer s.outputHub.End(rec.ID, actionID, EventDisconnected)

	scanner := bufio.NewScanner(r.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var frame companionOutputFrame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			continue
		}
		if frame.ActionID != actionID {
			continue
		}
		s.outputHub.Publish(rec.ID, actionID, frame.Line)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

// handleAdminOutputStream is the SSE endpoint a browser holds open to
// watch one agent's live action output -- mirrors handleCompanionStream's
// exact shape (flusher, heartbeat, context-done) but subscribes to
// outputHub instead of pushing Actions.
func (s *Server) handleAdminOutputStream(w http.ResponseWriter, r *http.Request, id string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ch, cancel := s.outputHub.Subscribe(id)
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.shutdownCtx.Done():
			// See shutdownCtx's own doc comment on Server.
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event := <-ch:
			payload, err := json.Marshal(struct {
				ActionID string `json:"action_id"`
				Line     string `json:"line,omitempty"`
			}{ActionID: event.ActionID, Line: event.Line})
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Kind, payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data := adminPageData{AggregatorVersion: version.Version}
	if s.selfUpdate != nil {
		data.SelfUpdateConfigured = true
		data.SelfUpdateIncludePreRelease = s.selfUpdate.IncludePreRelease()
		if latest, _, ok := s.selfUpdate.Latest(); ok {
			data.LatestVersion = latest
			if cmp, err := version.Compare(latest, version.Version); err == nil && cmp > 0 {
				data.AggregatorUpdateAvailable = true
			}
		}
	}
	for _, rec := range s.registry.List() {
		v := toAgentView(rec, s.hub, data.LatestVersion)
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

// handleAdminAction serves POST /admin/agents/{id}/approve and .../reject,
// among others, plus (the one GET among otherwise all-POST actions here)
// /admin/agents/{id}/output/stream.
// Revoking an approved agent reuses reject — there's no separate state for it.
func (s *Server) handleAdminAction(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/admin/agents/")
	parts := strings.Split(trimmed, "/")

	// Peeled off before the POST-only gate below, since this one's a GET
	// -- everything else this function dispatches to is POST-only.
	if r.Method == http.MethodGet && len(parts) == 3 && parts[1] == "output" && parts[2] == "stream" {
		s.handleAdminOutputStream(w, r, parts[0])
		return
	}
	if r.Method == http.MethodGet && len(parts) == 2 && parts[1] == "version" {
		s.handleAdminAgentVersion(w, r, parts[0])
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	if action == "self-update" {
		s.handleAdminSelfUpdate(w, r, id)
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
	s.outputHub.Begin(id, action.ID)

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
	s.outputHub.Begin(id, action.ID)

	writeJSON(w, http.StatusAccepted, map[string]string{"action_id": action.ID})
}

type selfUpdateRequest struct {
	Component     string `json:"component"`
	TargetVersion string `json:"target_version"`
}

// handleAdminSelfUpdate pushes an ActionSelfUpdate to the companion
// connected for id, asking it to update its own host's agent,
// aggregator (if one happens to be co-located there), or itself to
// TargetVersion. Gated by the same shared secret as handleAdminApply --
// self-update is at least as high-stakes as an apply, since it can
// restart the aggregator process serving this very request.
//
// The dependency-ordering rule lives here, not just in the admin page's
// UI: agent/companion must never be pushed to a version newer than the
// aggregator's own currently-running version.Version, to avoid the exact
// protocol-mismatch confusion a newer agent talking to an older
// aggregator can cause (confirmed live earlier -- a new agent connecting
// to an old aggregator that doesn't know about X-Client-Kind at all just
// gets silently mistreated as a companion). This is the actual trust
// boundary; greying out a button client-side is UX on top of this, not
// a substitute for it.
func (s *Server) handleAdminSelfUpdate(w http.ResponseWriter, r *http.Request, id string) {
	if s.adminApplySecret == "" {
		http.Error(w, "self-update is disabled: ADMIN_APPLY_SHARED_SECRET is not configured", http.StatusNotImplemented)
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

	var req selfUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	switch req.Component {
	case "agent", "aggregator", "companion":
	default:
		http.Error(w, "component must be one of: agent, aggregator, companion", http.StatusBadRequest)
		return
	}
	if req.TargetVersion == "" {
		http.Error(w, "target_version is required", http.StatusBadRequest)
		return
	}

	if req.Component != "aggregator" {
		cmp, err := version.Compare(req.TargetVersion, version.Version)
		if err != nil {
			http.Error(w, fmt.Sprintf("target_version %q: %v", req.TargetVersion, err), http.StatusBadRequest)
			return
		}
		if cmp > 0 {
			http.Error(w, fmt.Sprintf(
				"target_version %s is newer than this aggregator's own version %s -- update the aggregator first",
				req.TargetVersion, version.Version), http.StatusConflict)
			return
		}
	}

	action := Action{
		ID:            newActionID(),
		Type:          ActionSelfUpdate,
		Component:     req.Component,
		TargetVersion: req.TargetVersion,
		CreatedAt:     time.Now(),
	}
	if err := s.hub.Push(id, action); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.outputHub.Begin(id, action.ID)

	writeJSON(w, http.StatusAccepted, map[string]string{"action_id": action.ID})
}

// handleAdminAgentVersion reports id's currently-known agent/companion
// versions and its last report time, straight from the registry/hub --
// polled by the admin page's own JS after an action's live-output stream
// reports "done", instead of blindly reloading. Both an apply's
// out-of-band recheck and a self-update's restart/redeploy return long
// before the *new* agent process has actually finished its own detection
// cycle and reported back in -- for a self-update of "agent"/"companion"
// specifically, AgentVersion/CompanionVersion only change once that
// happens; for a plain package apply, the shrunk upgradable list only
// shows up in that same report. A fixed short delay before reloading
// routinely lands before either arrives, so the admin page polls this
// until LastSeen actually advances (or, for self-update, until the
// version actually matches) rather than guessing.
func (s *Server) handleAdminAgentVersion(w http.ResponseWriter, _ *http.Request, id string) {
	rec, ok := s.registry.Get(id)
	if !ok {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	agentVersion := ""
	if rec.LastReport != nil {
		agentVersion = rec.LastReport.AgentVersion
	}
	lastSeen := ""
	if !rec.LastSeen.IsZero() {
		lastSeen = rec.LastSeen.Format(time.RFC3339Nano)
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"agent_version":     agentVersion,
		"companion_version": s.hub.CompanionVersion(id),
		"last_seen":         lastSeen,
	})
}

type selfUpdateChannelRequest struct {
	IncludePreRelease bool `json:"include_prerelease"`
}

// handleAdminSelfUpdateChannel switches which release channel
// internal/selfupdate's Latest() considers "available" -- e.g. to test
// a pre-release cut against a live fleet without waiting for a real
// release. Gated by the same shared secret as apply/self-update: it
// can't change anything on a host directly, but it does control which
// "Update available" buttons appear, so it gets the same trust level as
// everything else that shapes what those buttons will do.
func (s *Server) handleAdminSelfUpdateChannel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.selfUpdate == nil {
		http.Error(w, "self-update checking is disabled", http.StatusNotImplemented)
		return
	}
	if s.adminApplySecret == "" {
		http.Error(w, "self-update is disabled: ADMIN_APPLY_SHARED_SECRET is not configured", http.StatusNotImplemented)
		return
	}
	presented := r.Header.Get("X-Admin-Apply-Secret")
	if presented == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(s.adminApplySecret)) != 1 {
		http.Error(w, "invalid or missing X-Admin-Apply-Secret", http.StatusForbidden)
		return
	}

	var req selfUpdateChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	s.selfUpdate.SetIncludePreRelease(req.IncludePreRelease)
	// Refresh synchronously so the very next GET /admin reflects the new
	// channel right away, instead of waiting for the next
	// SELF_UPDATE_CHECK_INTERVAL tick. A refresh failure here doesn't undo
	// the channel switch -- it just means the operator sees a stale
	// LatestVersion until the next successful refresh, same as any other
	// transient Refresh failure.
	resp := map[string]any{"include_prerelease": req.IncludePreRelease}
	if err := s.selfUpdate.Refresh(r.Context()); err != nil {
		resp["refresh_error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
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
