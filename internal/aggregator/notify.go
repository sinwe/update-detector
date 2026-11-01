package aggregator

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"update-detector/internal/checker"
	"update-detector/internal/notifier"
	"update-detector/internal/state"
	"update-detector/internal/version"
)

// HostNotifier delivers one event for one host, choosing between that
// host's own bot and the fleet-wide manager (Server implements it — see
// NotifyHost).
type HostNotifier interface {
	NotifyHost(ctx context.Context, rec AgentRecord, ev notifier.Event)
}

// notifySendTimeout bounds one per-host Telegram delivery. Deliberately
// short and independent of the request that triggered it: a slow or
// unreachable Bot API must never hold up an agent's /report.
const notifySendTimeout = 15 * time.Second

// SetNotifyStore attaches the per-host Telegram targets (see NotifyStore).
// Called once at startup; nil (tests, or an aggregator that never loaded
// one) simply means no host has its own bot and everything falls back to
// the fleet-wide TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID pair.
func (s *Server) SetNotifyStore(store *NotifyStore) {
	s.notifyStore = store
}

// NotifyHost delivers ev for rec, preferring that host's own bot when it
// has one configured and enabled, and falling back to the fleet-wide
// manager otherwise. This is the single place the two delivery paths
// meet: presence alerts and update alerts both go through it, so a host
// never gets the same message twice (its own bot *or* the fleet chat,
// never both).
func (s *Server) NotifyHost(ctx context.Context, rec AgentRecord, ev notifier.Event) {
	if ev.Hostname == "" {
		ev.Hostname = rec.Hostname
	}
	if s.notifyStore != nil {
		if cfg, ok := s.notifyStore.Get(rec.ID); ok && cfg.Active() {
			if err := s.newTelegram(cfg.BotToken, cfg.ChatID).Send(ctx, ev); err != nil {
				log.Printf("notify: per-host telegram for %s: %v", rec.Hostname, err)
			}
			return
		}
	}
	if s.notifyMgr != nil {
		s.notifyMgr.Send(ctx, ev)
	}
}

// maybeNotifyUpdate sends this host's update-change message when it has
// its own bot configured. Hosts without one are deliberately left alone:
// their own agent already notifies on the same diff (see
// cmd/update-detector/main.go), and sending from both sides would double
// every message.
func (s *Server) maybeNotifyUpdate(rec AgentRecord, previous *checker.Status, current checker.Status) {
	if s.notifyStore == nil {
		return
	}
	cfg, ok := s.notifyStore.Get(rec.ID)
	if !ok || !cfg.Active() {
		return
	}
	changes := state.Diff(previous, current)
	if len(changes) == 0 {
		return
	}
	// A fresh, bounded context: r.Context() is cancelled the moment the
	// /report handler returns, which would abort the send mid-flight.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifySendTimeout)
		defer cancel()
		s.NotifyHost(ctx, rec, notifier.Event{
			Hostname: rec.Hostname,
			Status:   current,
			Previous: previous,
			Changes:  changes,
		})
	}()
}

// notifyHostView is one host's row on /admin/notifications.
type notifyHostView struct {
	ID         string
	Hostname   string
	RemoteAddr string
	// Configured is whether a token+chat pair exists at all;
	// Active additionally requires the checkbox to be on.
	Configured  bool
	Active      bool
	TokenMasked string
	ChatID      string
	UpdatedAt   string
	// FleetDefault marks hosts with no own bot: they inherit the
	// fleet-wide pair, when one is configured.
	FleetDefault bool
}

type notifyPageData struct {
	AggregatorVersion  string
	Hosts              []notifyHostView
	FleetBotConfigured bool
	// Message/Error carry one-shot feedback across the POST/redirect/GET
	// round trip (?ok= / ?err=).
	Message string
	Error   string
}

// handleAdminNotifications serves GET /admin/notifications (the page) and
// POST (save / clear / test for one host).
//
// Trust is the same as the rest of /admin: none beyond the network path.
// That matters more here than elsewhere — whoever can reach this page can
// point a host's alerts at their own bot — so the page says so, and
// /admin is expected to stay on a trusted network (or behind a
// reverse-proxy auth layer, as docs/reference.md already recommends for
// apply).
func (s *Server) handleAdminNotifications(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.renderNotificationsPage(w, r)
	case http.MethodPost:
		s.handleNotificationsPost(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) renderNotificationsPage(w http.ResponseWriter, r *http.Request) {
	data := notifyPageData{
		AggregatorVersion: version.Version,
		Message:           r.URL.Query().Get("ok"),
		Error:             r.URL.Query().Get("err"),
	}
	data.FleetBotConfigured = len(s.notifyChannels) > 0
	var snapshot map[string]HostNotifyConfig
	if s.notifyStore != nil {
		snapshot = s.notifyStore.Snapshot()
	}
	for _, rec := range s.registry.List() {
		if rec.Status != StatusApproved {
			continue
		}
		v := notifyHostView{ID: rec.ID, Hostname: rec.Hostname, RemoteAddr: rec.LastRemoteAddr}
		if cfg, ok := snapshot[rec.ID]; ok {
			v.Configured = cfg.Configured()
			v.Active = cfg.Active()
			v.TokenMasked = cfg.MaskedToken()
			v.ChatID = cfg.ChatID
			if !cfg.UpdatedAt.IsZero() {
				v.UpdatedAt = cfg.UpdatedAt.Format(time.RFC3339)
			}
		}
		v.FleetDefault = !v.Active
		data.Hosts = append(data.Hosts, v)
	}
	sort.Slice(data.Hosts, func(i, j int) bool {
		return strings.ToLower(data.Hosts[i].Hostname) < strings.ToLower(data.Hosts[j].Hostname)
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := renderNotifyPage(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleNotificationsPost applies one form submission. Deliberately
// form-encoded and redirect-after-post rather than fetch/JSON: the page
// works without JS, and a reload can never resubmit a token.
func (s *Server) handleNotificationsPost(w http.ResponseWriter, r *http.Request) {
	if s.notifyStore == nil {
		http.Error(w, "per-host notifications unavailable", http.StatusNotImplemented)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form body", http.StatusBadRequest)
		return
	}
	agentID := strings.TrimSpace(r.PostFormValue("agent_id"))
	action := strings.TrimSpace(r.PostFormValue("action"))
	rec, ok := s.registry.Get(agentID)
	if !ok || rec.Status != StatusApproved {
		s.redirectNotify(w, r, "", "unknown or unapproved host")
		return
	}

	switch action {
	case "clear":
		if err := s.notifyStore.Delete(agentID); err != nil {
			s.redirectNotify(w, r, "", err.Error())
			return
		}
		s.redirectNotify(w, r, rec.Hostname+": cleared, using the fleet-wide bot", "")
	case "save":
		cfg, err := hostNotifyFromForm(r, s.notifyStore)
		if err != nil {
			s.redirectNotify(w, r, "", rec.Hostname+": "+err.Error())
			return
		}
		if err := s.notifyStore.Set(agentID, cfg); err != nil {
			s.redirectNotify(w, r, "", rec.Hostname+": "+err.Error())
			return
		}
		state := "saved (enabled)"
		if !cfg.Enabled {
			state = "saved (paused, using the fleet-wide bot)"
		}
		s.redirectNotify(w, r, rec.Hostname+": "+state, "")
	case "test":
		cfg, err := hostNotifyFromForm(r, s.notifyStore)
		if err != nil {
			s.redirectNotify(w, r, "", rec.Hostname+": "+err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), notifySendTimeout)
		defer cancel()
		ev := notifier.Event{
			Hostname: rec.Hostname,
			Title:    "test notification",
			Changes:  []string{"this is a test from the aggregator's Notification page"},
			Brief:    true,
		}
		if err := s.newTelegram(cfg.BotToken, cfg.ChatID).Send(ctx, ev); err != nil {
			s.redirectNotify(w, r, "", rec.Hostname+": test failed: "+err.Error())
			return
		}
		s.redirectNotify(w, r, rec.Hostname+": test message sent", "")
	default:
		s.redirectNotify(w, r, "", "unknown action")
	}
}

// hostNotifyFromForm reads and validates the two credential fields. A
// blank token on save keeps the stored one (the page only ever shows a
// masked token, so an operator editing just the chat id must not have to
// retype the secret).
func hostNotifyFromForm(r *http.Request, store *NotifyStore) (HostNotifyConfig, error) {
	token := strings.TrimSpace(r.PostFormValue("bot_token"))
	chatID := strings.TrimSpace(r.PostFormValue("chat_id"))
	enabled := r.PostFormValue("enabled") != ""

	if token == "" {
		agentID := strings.TrimSpace(r.PostFormValue("agent_id"))
		if existing, ok := store.Get(agentID); ok {
			token = existing.BotToken
		}
	}
	if token == "" || chatID == "" {
		return HostNotifyConfig{}, errNotifyMissingFields
	}
	if !looksLikeBotToken(token) {
		return HostNotifyConfig{}, errNotifyBadToken
	}
	if _, err := strconv.ParseInt(chatID, 10, 64); err != nil {
		return HostNotifyConfig{}, errNotifyBadChatID
	}
	return HostNotifyConfig{BotToken: token, ChatID: chatID, Enabled: enabled}, nil
}

type notifyFormError string

func (e notifyFormError) Error() string { return string(e) }

const (
	errNotifyMissingFields notifyFormError = "bot token and chat id are both required"
	errNotifyBadToken      notifyFormError = "bot token doesn't look like <bot-id>:<secret> (copy it from @BotFather)"
	errNotifyBadChatID     notifyFormError = "chat id must be a number (groups and channels are negative, e.g. -1001234567890)"
)

// looksLikeBotToken checks the shape BotFather issues: "<digits>:<secret>".
// Not a validity check — only the Bot API can say that, and the Test
// button is how an operator finds out.
func looksLikeBotToken(token string) bool {
	id, secret, ok := strings.Cut(token, ":")
	if !ok || secret == "" {
		return false
	}
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return false
	}
	return true
}

func (s *Server) redirectNotify(w http.ResponseWriter, r *http.Request, ok, errMsg string) {
	q := url.Values{}
	if ok != "" {
		q.Set("ok", ok)
	}
	if errMsg != "" {
		q.Set("err", errMsg)
	}
	target := "/admin/notifications"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
