package aggregator

import (
	"html/template"
	"io"
	"time"

	"update-detector/internal/checker"
	"update-detector/internal/version"
)

var adminTemplate = template.Must(template.New("admin").Parse(adminTemplateSrc))

// agentView is the template-friendly projection of an AgentRecord — plain
// strings/bools instead of time.Time/pointers, so the template stays free
// of custom functions.
type agentView struct {
	ID                 string
	ShortID            string
	Hostname           string
	FirstSeen          string
	LastSeen           string
	HasReport          bool
	OK                 bool
	Errors             []string
	UpgradableTotal    int
	UpgradableSecurity int
	Upgrades           []checker.PackageUpgrade
	RebootRequired     bool
	OSUpdateAvailable  bool
	AgentVersion       string

	// AnyStreamConnected, CompanionConnected, CompanionVersion, and
	// RecentResults are only meaningful once Approved -- nothing has
	// anything to connect to, or report on, before that.
	//
	// AnyStreamConnected is true whether the connected stream is the
	// companion or the agent itself (see ClientKind) -- shown in the
	// Agent column, gating Force-recheck's display, since recheck works
	// over either kind of stream (a connected companion also relays it
	// to the agent locally -- see execute.go's own triggerRecheck).
	// CompanionConnected is strictly kind==companion -- shown in the
	// separate Companion column, gating apply-only controls, since only
	// a real companion can run apt-get/winget. These used to render
	// together in one column with a "(via agent)" qualifier on
	// "connected" to disambiguate which kind was live; kept apart now so
	// the Agent column's own connectivity is never displayed as if it
	// were the companion's.
	AnyStreamConnected bool
	CompanionConnected bool
	CompanionVersion   string
	RecentResults      []resultView

	// AggregatorPresent is whether this row's own companion last reported
	// detecting the aggregator itself running (natively or as a Docker
	// container) on the same host -- gates the "Update aggregator" button
	// so it doesn't show up on every host with a connected companion,
	// only ones where clicking it wouldn't just fail immediately (see
	// SelfUpdate's DeployNone fallthrough).
	AggregatorPresent bool

	// AgentPresent is whether this row's own companion last reported
	// detecting agent running (natively or as a Docker container)
	// on this same host.
	AgentPresent bool

	// AgentUpdateAvailable/CompanionUpdateAvailable compare this row's own
	// reported agent/companion version against adminPageData.LatestVersion
	// -- false whenever LatestVersion is empty (no successful self-update
	// check has completed yet), the reported version doesn't parse, or
	// the aggregator itself is behind (see toAgentView's aggregatorBehind
	// -- the server would reject that request anyway).
	AgentUpdateAvailable     bool
	CompanionUpdateAvailable bool

	// PendingActionID is the action currently in flight for this agent,
	// if any (mirrors CompanionHub's own pending map) -- lets a page
	// load/reload auto-resume watching its live output, not just the tab
	// that originally triggered it.
	PendingActionID string
}

type resultView struct {
	Success     bool
	Message     string
	CompletedAt string
}

type adminPageData struct {
	AggregatorVersion string
	// LatestVersion is the latest release internal/selfupdate has found,
	// or "" if no successful check has completed yet.
	LatestVersion             string
	AggregatorUpdateAvailable bool
	// SelfUpdateConfigured is false when the aggregator was built without
	// a selfupdate.Client at all -- hides the channel toggle entirely
	// rather than showing a control that would just 501 on every use.
	SelfUpdateConfigured bool
	// SelfUpdateChannel mirrors selfupdate.Client's own current channel
	// (SELF_UPDATE_CHANNEL at startup, or whatever an operator has since
	// switched it to via the selector below) -- one of version.Channels.
	SelfUpdateChannel string
	Pending                     []agentView
	Approved                    []agentView
	Rejected                    []agentView
}

// updateAvailable reports whether latest (adminPageData.LatestVersion,
// possibly "") is a newer release than current (a specific host's own
// reported agent/companion version, possibly "" if never reported) --
// false, not an error, for anything unset or unparseable, since this
// only ever gates a UI affordance, never a trust decision (that
// enforcement lives server-side in handleAdminSelfUpdate itself).
func updateAvailable(latest, current string) bool {
	if latest == "" || current == "" {
		return false
	}
	cmp, err := version.Compare(latest, current)
	return err == nil && cmp > 0
}

func toAgentView(rec AgentRecord, hub *CompanionHub, latestVersion string) agentView {
	kind, connected := hub.Kind(rec.ID)
	companionVersion := hub.CompanionVersion(rec.ID)
	// aggregatorBehind mirrors handleAdminSelfUpdate's own dependency-
	// ordering rule (component != "aggregator" is rejected, 409, when
	// TargetVersion is newer than the aggregator's own running version):
	// while the aggregator itself is behind, any agent/companion update
	// button would just target a version the server will refuse, so
	// don't offer it at all -- "Update aggregator" is the only real
	// option until that's no longer true.
	aggregatorBehind := updateAvailable(latestVersion, version.Version)
	v := agentView{
		ID:                       rec.ID,
		ShortID:                  shortID(rec.ID),
		Hostname:                 rec.Hostname,
		AnyStreamConnected:       connected,
		CompanionConnected:       connected && kind == KindCompanion,
		CompanionVersion:         companionVersion,
		CompanionUpdateAvailable: updateAvailable(latestVersion, companionVersion) && !aggregatorBehind,
		AggregatorPresent:        hub.AggregatorPresent(rec.ID),
		AgentPresent:             hub.AgentPresent(rec.ID),
	}
	if actionID, ok := hub.Pending(rec.ID); ok {
		v.PendingActionID = actionID
	}
	if !rec.FirstSeen.IsZero() {
		v.FirstSeen = rec.FirstSeen.Format(time.RFC3339)
	}
	if !rec.LastSeen.IsZero() {
		v.LastSeen = rec.LastSeen.Format(time.RFC3339)
	}
	if rec.LastReport != nil {
		v.HasReport = true
		v.OK = rec.LastReport.OK
		v.Errors = rec.LastReport.Errors
		v.UpgradableTotal = rec.LastReport.Packages.UpgradableTotal
		v.UpgradableSecurity = rec.LastReport.Packages.UpgradableSecurity
		v.Upgrades = rec.LastReport.Packages.Upgrades
		v.RebootRequired = rec.LastReport.RebootRequired
		v.OSUpdateAvailable = rec.LastReport.OS.UpdateAvailable
		v.AgentVersion = rec.LastReport.AgentVersion
		v.AgentUpdateAvailable = updateAvailable(latestVersion, v.AgentVersion) && !aggregatorBehind
	}
	// Results() is oldest-first; show most recent first.
	results := hub.Results(rec.ID)
	for i := len(results) - 1; i >= 0; i-- {
		r := results[i]
		v.RecentResults = append(v.RecentResults, resultView{
			Success:     r.Success,
			Message:     r.Message,
			CompletedAt: r.CompletedAt.Format(time.RFC3339),
		})
	}
	return v
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func renderAdminPage(w io.Writer, data adminPageData) error {
	return adminTemplate.Execute(w, data)
}

const adminTemplateSrc = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Update Detector</title>
  <style>
    :root {
      --bg: #f8f9fa; --surface: #fff; --surface-hover: #f1f3f5;
      --border: #e9ecef; --border-strong: #dee2e6;
      --text: #212529; --text-secondary: #868e96; --text-muted: #adb5bd;
      --green: #40c057; --green-bg: #d3f9d8; --green-text: #2b8a3e;
      --red: #fa5252; --red-bg: #ffe3e3; --red-text: #c92a2a;
      --orange: #fd7e14; --orange-bg: #fff4e6; --orange-text: #d9480f;
      --blue: #4c6ef5; --blue-bg: #dbe4ff; --blue-text: #364fc7;
      --radius: 10px; --radius-sm: 6px;
      --shadow: 0 1px 3px rgba(0,0,0,.06), 0 1px 2px rgba(0,0,0,.04);
      --shadow-md: 0 4px 12px rgba(0,0,0,.08);
      --transition: .15s ease;
    }
    @media(prefers-color-scheme:dark) {
      :root {
        --bg: #1a1b1e; --surface: #25262b; --surface-hover: #2c2e33;
        --border: #373840; --border-strong: #47484e;
        --text: #e4e5e7; --text-secondary: #909296; --text-muted: #5c5e66;
        --green: #51cf66; --green-bg: #1b3a21; --green-text: #69db7c;
        --red: #ff6b6b; --red-bg: #3a1b1b; --red-text: #ffa8a8;
        --orange: #ffa94d; --orange-bg: #3a2b1b; --orange-text: #ffd8a8;
        --blue: #748ffc; --blue-bg: #1b243a; --blue-text: #91a7ff;
        --shadow: 0 1px 3px rgba(0,0,0,.3); --shadow-md: 0 4px 12px rgba(0,0,0,.4);
      }
    }
    *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
      background: var(--bg); color: var(--text); line-height: 1.5;
      min-height: 100dvh; -webkit-font-smoothing: antialiased;
    }
    a { color: var(--blue); text-decoration: none; } a:hover { text-decoration: underline; }
    button {
      cursor: pointer; border: 1px solid var(--border-strong); border-radius: var(--radius-sm);
      padding: .35rem .75rem; font-size: .813rem; font-weight: 500;
      background: var(--surface); color: var(--text); transition: var(--transition);
      display: inline-flex; align-items: center; gap: .3rem; white-space: nowrap;
    }
    button:hover { background: var(--surface-hover); }
    button:active { transform: scale(.97); }
    form { display: inline; }
    .btn-primary { background: var(--blue); color: #fff; border-color: var(--blue); }
    .btn-primary:hover { filter: brightness(1.1); background: var(--blue); }
    .btn-danger { background: var(--red); color: #fff; border-color: var(--red); }
    .btn-danger:hover { filter: brightness(1.1); background: var(--red); }
    .btn-success { background: var(--green); color: #fff; border-color: var(--green); }
    .btn-success:hover { filter: brightness(1.1); background: var(--green); }
    .btn-warn { background: var(--orange); color: #fff; border-color: var(--orange); }
    .btn-warn:hover { filter: brightness(1.1); background: var(--orange); }
    .btn-sm { padding: .2rem .5rem; font-size: .75rem; }

    /* Layout */
    .container { max-width: 960px; margin: 0 auto; padding: 1rem; }
    @media(min-width:640px) { .container { padding: 1.5rem; } }

    /* Header */
    .header {
      display: flex; align-items: center; justify-content: space-between;
      flex-wrap: wrap; gap: .5rem; margin-bottom: 1rem;
    }
    .header h1 { font-size: 1.25rem; font-weight: 700; display: flex; align-items: center; gap: .5rem; }
    .header h1 svg { width: 24px; height: 24px; }
    .version-badge {
      font-size: .7rem; font-weight: 600; background: var(--blue-bg); color: var(--blue-text);
      padding: .15rem .45rem; border-radius: 99px; letter-spacing: .02em;
    }

    /* Banners */
    .banner {
      padding: .75rem 1rem; border-radius: var(--radius); margin-bottom: .75rem;
      font-size: .875rem; display: flex; align-items: flex-start; gap: .5rem;
    }
    .banner-warn { background: var(--orange-bg); color: var(--orange-text); }
    .banner-info { background: var(--blue-bg); color: var(--blue-text); }
    .restart-banner {
      display: none; position: sticky; top: 0; z-index: 100;
      background: var(--green); color: #fff; padding: .75rem 1rem;
      border-radius: var(--radius); margin-bottom: .75rem;
      font-size: .875rem; font-weight: 500;
    }

    /* Channel toggle */
    .channel-row {
      display: flex; align-items: center; gap: .75rem; flex-wrap: wrap;
      font-size: .813rem; color: var(--text-secondary); margin-bottom: .75rem;
    }
    .channel-row label {
      display: inline-flex; align-items: center; gap: .25rem; cursor: pointer;
      padding: .2rem .5rem; border-radius: var(--radius-sm); transition: var(--transition);
    }
    .channel-row label:hover { background: var(--surface-hover); }
    .channel-row input[type=radio] { accent-color: var(--blue); }

    /* Links row */
    .links-row {
      display: flex; gap: .75rem; flex-wrap: wrap; font-size: .813rem;
      margin-bottom: 1rem; padding-bottom: .75rem; border-bottom: 1px solid var(--border);
    }

    /* Section */
    .section { margin-bottom: 1.5rem; }
    .section-head {
      display: flex; align-items: center; justify-content: space-between;
      margin-bottom: .5rem;
    }
    .section-title {
      font-size: .938rem; font-weight: 600; color: var(--text-secondary);
      text-transform: uppercase; letter-spacing: .04em;
    }
    .section-count {
      font-size: .75rem; font-weight: 700; background: var(--border);
      color: var(--text-secondary); padding: .1rem .5rem; border-radius: 99px;
    }

    /* Empty state */
    .empty {
      text-align: center; padding: 1.5rem; color: var(--text-muted);
      font-size: .875rem;
    }

    /* Host cards */
    .host-card {
      background: var(--surface); border: 1px solid var(--border);
      border-radius: var(--radius); padding: 1rem; margin-bottom: .5rem;
      box-shadow: var(--shadow); transition: var(--transition);
    }
    .host-card:hover { box-shadow: var(--shadow-md); }
    .host-top {
      display: flex; align-items: center; justify-content: space-between;
      flex-wrap: wrap; gap: .5rem; margin-bottom: .5rem;
    }
    .host-name {
      font-weight: 600; font-size: 1rem; display: flex; align-items: center; gap: .4rem;
    }
    .host-id { font-size: .7rem; color: var(--text-muted); font-family: monospace; }
    .host-badges { display: flex; gap: .35rem; flex-wrap: wrap; }

    /* Status badges */
    .badge {
      font-size: .688rem; font-weight: 600; padding: .15rem .5rem;
      border-radius: 99px; display: inline-flex; align-items: center; gap: .25rem;
      white-space: nowrap;
    }
    .badge-ok { background: var(--green-bg); color: var(--green-text); }
    .badge-bad { background: var(--red-bg); color: var(--red-text); }
    .badge-warn { background: var(--orange-bg); color: var(--orange-text); }
    .badge-info { background: var(--blue-bg); color: var(--blue-text); }
    .badge-muted { background: var(--border); color: var(--text-muted); }
    .badge::before {
      content: ''; width: 6px; height: 6px; border-radius: 50%;
      display: inline-block; flex-shrink: 0;
    }
    .badge-ok::before { background: var(--green); }
    .badge-bad::before { background: var(--red); }
    .badge-warn::before { background: var(--orange); }
    .badge-info::before { background: var(--blue); }
    .badge-muted::before { background: var(--text-muted); }

    /* Host details */
    .host-meta {
      display: flex; gap: 1rem; flex-wrap: wrap; font-size: .813rem;
      color: var(--text-secondary); margin-bottom: .5rem;
    }
    .host-meta span { display: inline-flex; align-items: center; gap: .25rem; }
    .host-report {
      font-size: .813rem; color: var(--text-secondary);
      display: flex; flex-wrap: wrap; gap: .5rem; align-items: baseline;
    }
    .host-report strong { color: var(--text); }

    /* Actions row */
    .host-actions {
      display: flex; flex-wrap: wrap; gap: .35rem; margin-top: .5rem;
      padding-top: .5rem; border-top: 1px solid var(--border);
    }

    /* Details/summary */
    details { margin-top: .25rem; }
    summary {
      cursor: pointer; font-size: .813rem; color: var(--blue);
      padding: .2rem 0; user-select: none;
    }
    summary:hover { text-decoration: underline; }
    details[open] summary { margin-bottom: .25rem; }
    .pkg-list {
      list-style: none; font-size: .813rem; display: flex; flex-direction: column; gap: .2rem;
    }
    .pkg-list li {
      padding: .2rem .5rem; border-radius: var(--radius-sm);
      display: flex; align-items: center; gap: .4rem;
    }
    .pkg-list li:hover { background: var(--surface-hover); }
    .pkg-sec { color: var(--red-text); font-weight: 600; }
    .pkg-arrow { color: var(--text-muted); }
    .pkg-ver { font-family: monospace; font-size: .75rem; color: var(--text-secondary); }

    /* Apply form */
    .apply-form { font-size: .813rem; }
    .apply-form label {
      display: flex; align-items: center; gap: .3rem; padding: .15rem 0; cursor: pointer;
    }
    .apply-form input[type=checkbox] { accent-color: var(--blue); }

    /* Output pane */
    .output-pane {
      display: none; background: #1e1e1e; color: #d4d4d4;
      font-family: 'SF Mono', 'Fira Code', 'Cascadia Code', monospace;
      font-size: .75rem; padding: .75rem; margin-top: .5rem;
      max-height: 240px; overflow-y: auto; white-space: pre-wrap;
      border-radius: var(--radius-sm); line-height: 1.6;
    }

    /* Recent results */
    .results-list { list-style: none; font-size: .813rem; }
    .results-list li {
      padding: .2rem 0; display: flex; gap: .5rem; align-items: baseline;
      color: var(--text-secondary);
    }
    .results-list .res-ok { color: var(--green-text); }
    .results-list .res-bad { color: var(--red-text); }

    /* Pending cards (simpler) */
    .pending-card {
      background: var(--surface); border: 1px solid var(--border);
      border-radius: var(--radius); padding: .75rem 1rem;
      margin-bottom: .5rem; box-shadow: var(--shadow);
      display: flex; align-items: center; justify-content: space-between;
      flex-wrap: wrap; gap: .5rem;
    }
    .pending-info { display: flex; align-items: center; gap: .75rem; flex-wrap: wrap; }
    .pending-info .host-name { font-size: .938rem; }
    .pending-actions { display: flex; gap: .35rem; }

    /* Rejected cards */
    .rejected-card {
      background: var(--surface); border: 1px solid var(--border);
      border-radius: var(--radius); padding: .75rem 1rem;
      margin-bottom: .5rem; box-shadow: var(--shadow);
      display: flex; align-items: center; justify-content: space-between;
      flex-wrap: wrap; gap: .5rem; opacity: .7;
    }

    /* Scrollbar */
    .output-pane::-webkit-scrollbar { width: 6px; }
    .output-pane::-webkit-scrollbar-track { background: transparent; }
    .output-pane::-webkit-scrollbar-thumb { background: #555; border-radius: 3px; }
  </style>
</head>
<body>
  <div class="container">
    <div id="restartBanner" class="restart-banner">Updating the aggregator&hellip; this page will reload automatically once it's back.</div>

    <div class="header">
      <h1>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 11-6.219-8.56"/><polyline points="21 3 21 9 15 9"/><path d="M12 8v4l3 3"/></svg>
        Update Detector
        <span class="version-badge">{{.AggregatorVersion}}</span>
      </h1>
    </div>

    {{if .SelfUpdateConfigured}}
    <div class="channel-row">
      Channel:
      <label><input type="radio" name="selfUpdateChannel" value="alpha" {{if eq .SelfUpdateChannel "alpha"}}checked{{end}} onchange="postSelfUpdateChannel('alpha')"> Alpha</label>
      <label><input type="radio" name="selfUpdateChannel" value="beta" {{if eq .SelfUpdateChannel "beta"}}checked{{end}} onchange="postSelfUpdateChannel('beta')"> Beta</label>
      <label><input type="radio" name="selfUpdateChannel" value="rc" {{if eq .SelfUpdateChannel "rc"}}checked{{end}} onchange="postSelfUpdateChannel('rc')"> RC</label>
      <label><input type="radio" name="selfUpdateChannel" value="release" {{if eq .SelfUpdateChannel "release"}}checked{{end}} onchange="postSelfUpdateChannel('release')"> Release</label>
    </div>
    {{end}}

    {{if .AggregatorUpdateAvailable}}
    <div class="banner banner-warn">
      <strong>{{.LatestVersion}}</strong> is available (running <strong>{{.AggregatorVersion}}</strong>).
      Use "Update aggregator" from the host that runs it.
    </div>
    {{end}}

    <div class="links-row">
      <a href="/widgets/packages">All packages</a>
      <a href="/widgets/packages?security=true">Security only</a>
      <a href="/widgets/summary">Summary</a>
    </div>

    {{if .Pending}}
    <div class="section">
      <div class="section-head">
        <span class="section-title">Pending</span>
        <span class="section-count">{{len .Pending}}</span>
      </div>
      {{range .Pending}}
      <div class="pending-card">
        <div class="pending-info">
          <span class="host-name">{{.Hostname}}</span>
          <span class="host-id">{{.ShortID}}</span>
          <span class="badge badge-muted">{{.FirstSeen}}</span>
        </div>
        <div class="pending-actions">
          <form method="post" action="/admin/agents/{{.ID}}/approve"><button class="btn-success btn-sm">Approve</button></form>
          <form method="post" action="/admin/agents/{{.ID}}/reject"><button class="btn-danger btn-sm">Reject</button></form>
        </div>
      </div>
      {{end}}
    </div>
    {{end}}

    <div class="section">
      <div class="section-head">
        <span class="section-title">Hosts</span>
        <span class="section-count">{{len .Approved}}</span>
      </div>
      {{if .Approved}}
      {{range .Approved}}
      <div class="host-card">
        <div class="host-top">
          <div>
            <span class="host-name">{{.Hostname}}</span>
            <span class="host-id">{{.ShortID}}</span>
          </div>
          <div class="host-badges">
            {{if .HasReport}}
              {{if .OK}}<span class="badge badge-ok">OK</span>{{else}}<span class="badge badge-bad">Needs attention</span>{{end}}
              {{if .RebootRequired}}<span class="badge badge-warn">Reboot</span>{{end}}
              {{if .OSUpdateAvailable}}<span class="badge badge-warn">OS upgrade</span>{{end}}
            {{else}}
              <span class="badge badge-muted">No report</span>
            {{end}}
            {{if .AnyStreamConnected}}<span class="badge badge-info">Agent</span>{{else}}<span class="badge badge-muted">not connected</span>{{end}}
            {{if .CompanionConnected}}<span class="badge badge-ok">Companion</span>{{end}}
          </div>
        </div>

        {{if .HasReport}}
        <div class="host-meta">
          {{if .AgentVersion}}<span>Agent {{.AgentVersion}}</span>{{end}}
          {{if .CompanionConnected}}{{if .CompanionVersion}}<span>Companion {{.CompanionVersion}}</span>{{end}}{{end}}
        </div>
        <div class="host-report">
          <span><strong>{{.UpgradableTotal}}</strong> upgradable</span>
          <span><strong>{{.UpgradableSecurity}}</strong> security</span>
          <a href="/widgets/hosts/{{.Hostname}}" style="font-size:.75rem">JSON</a>
        </div>

        {{if .Upgrades}}
        <details>
          <summary>{{len .Upgrades}} package{{if gt (len .Upgrades) 1}}s{{end}}</summary>
          <ul class="pkg-list">
            {{range .Upgrades}}
            <li>
              {{if .Security}}<span class="pkg-sec">{{.Name}}</span>{{else}}{{.Name}}{{end}}
              {{if .CurrentVersion}}<span class="pkg-ver">{{.CurrentVersion}}</span>{{end}}
              <span class="pkg-arrow">&rarr;</span>
              <span class="pkg-ver">{{.CandidateVersion}}</span>
            </li>
            {{end}}
          </ul>
        </details>
        {{end}}

        {{if .Errors}}
        <div style="font-size:.75rem;color:var(--red-text);margin-top:.25rem">
          {{range .Errors}}{{.}}; {{end}}
        </div>
        {{end}}
        {{end}}

        <div class="host-actions">
          <button class="btn-sm" onclick="forceRecheck('{{.ID}}')" title="Re-scan now">Force recheck</button>
          {{if .CompanionConnected}}
            <button class="btn-primary btn-sm" onclick="applyAction('{{.ID}}', 'upgrade')">Upgrade all</button>
            <button class="btn-warn btn-sm" onclick="applyAction('{{.ID}}', 'full-upgrade')">Full upgrade all</button>
            {{if .AgentUpdateAvailable}}
            <button class="btn-sm" onclick="postSelfUpdate('{{.ID}}', 'agent', '{{$.LatestVersion}}')" title="Update agent to {{$.LatestVersion}}">Update agent</button>
            {{end}}
            {{if and $.AggregatorUpdateAvailable .AggregatorPresent}}
            <button class="btn-sm" onclick="postSelfUpdate('{{.ID}}', 'aggregator', '{{$.LatestVersion}}')" title="Update aggregator to {{$.LatestVersion}}">Update aggregator</button>
            {{end}}
            {{if .CompanionUpdateAvailable}}
            <button class="btn-sm" onclick="postSelfUpdate('{{.ID}}', 'companion', '{{$.LatestVersion}}')" title="Update companion to {{$.LatestVersion}}">Update companion</button>
            {{end}}
          {{else}}
            <span class="badge badge-muted">install companion to enable apply</span>
          {{end}}
          <span style="flex:1"></span>
          <form method="post" action="/admin/agents/{{.ID}}/reject"><button class="btn-danger btn-sm">Revoke</button></form>
        </div>

        {{if .CompanionConnected}}{{if .Upgrades}}
        <details>
          <summary>Select packages to apply</summary>
          <form class="apply-form" onsubmit="return applyPackages(event, '{{.ID}}')">
            {{range .Upgrades}}
            <label><input type="checkbox" name="pkg" value="{{.Name}}"> {{if .Security}}<span class="pkg-sec">{{.Name}}</span>{{else}}{{.Name}}{{end}}</label>
            {{end}}
            <button type="submit" class="btn-primary btn-sm" style="margin-top:.35rem">Apply selected</button>
          </form>
        </details>
        {{end}}{{end}}

        <pre id="output-{{.ID}}" class="output-pane" data-agent-id="{{.ID}}"{{if .PendingActionID}} data-pending-action-id="{{.PendingActionID}}"{{end}}></pre>

        {{if .RecentResults}}
        <details>
          <summary>recent actions ({{len .RecentResults}})</summary>
          <ul class="results-list">
            {{range .RecentResults}}
            <li>
              <span style="font-family:monospace;font-size:.75rem">{{.CompletedAt}}</span>
              {{if .Success}}<span class="res-ok">success</span>{{else}}<span class="res-bad">failed</span>{{end}}
              {{.Message}}
            </li>
            {{end}}
          </ul>
        </details>
        {{end}}
      </div>
      {{end}}
      {{else}}
      <div class="empty">No approved hosts yet</div>
      {{end}}
    </div>

    {{if .Rejected}}
    <div class="section">
      <div class="section-head">
        <span class="section-title">Rejected</span>
        <span class="section-count">{{len .Rejected}}</span>
      </div>
      {{range .Rejected}}
      <div class="rejected-card">
        <div class="pending-info">
          <span class="host-name">{{.Hostname}}</span>
          <span class="host-id">{{.ShortID}}</span>
        </div>
        <form method="post" action="/admin/agents/{{.ID}}/approve"><button class="btn-success btn-sm">Approve</button></form>
      </div>
      {{end}}
    </div>
    {{end}}
  </div>

  <script>
    function getAdminApplySecret() {
      let secret = localStorage.getItem('adminApplySecret');
      if (!secret) {
        secret = prompt('Enter the aggregator\'s ADMIN_APPLY_SHARED_SECRET (see README):');
        if (secret) localStorage.setItem('adminApplySecret', secret);
      }
      return secret;
    }

    function openLiveOutput(id, selfUpdateExpect) {
      const pane = document.getElementById('output-' + id);
      if (!pane) return null;
      pane.style.display = 'block';
      pane.textContent = '';
      const baselinePromise = selfUpdateExpect ? null : fetchAgentVersionInfo(id).then(d => d ? d.last_seen : '');
      const es = new EventSource('/admin/agents/' + id + '/output/stream');
      es.addEventListener('line', (e) => {
        const data = JSON.parse(e.data);
        pane.textContent += data.line + '\n';
        pane.scrollTop = pane.scrollHeight;
      });
      es.addEventListener('done', async () => {
        es.close();
        if (selfUpdateExpect) {
          pane.textContent += '--- done -- waiting for ' + selfUpdateExpect.component +
            ' to report version ' + selfUpdateExpect.targetVersion + ' ---\n';
          watchComponentVersion(id, selfUpdateExpect, pane);
        } else {
          pane.textContent += '--- done -- waiting for the fresh report to land ---\n';
          watchReportUpdated(id, await baselinePromise, pane);
        }
      });
      es.addEventListener('disconnected', () => {
        pane.textContent += '--- companion disconnected -- waiting for it to come back ---\n';
        es.close();
      });
      return es;
    }

    function closeLiveOutput(id, es) {
      if (es) es.close();
      const pane = document.getElementById('output-' + id);
      if (pane) pane.style.display = 'none';
    }

    async function fetchAgentVersionInfo(id) {
      try {
        const resp = await fetch('/admin/agents/' + id + '/version', {cache: 'no-store'});
        if (resp.ok) return await resp.json();
      } catch (e) {}
      return null;
    }

    function watchComponentVersion(id, expect, pane) {
      const field = expect.component === 'companion' ? 'companion_version' : 'agent_version';
      let attempts = 0;
      const maxAttempts = 60;
      const poll = setInterval(async () => {
        attempts++;
        const data = await fetchAgentVersionInfo(id);
        if (data && data[field] === expect.targetVersion) {
          clearInterval(poll);
          location.reload();
          return;
        }
        if (attempts >= maxAttempts) {
          clearInterval(poll);
          pane.textContent += '--- still on the old version after 2 minutes -- reload manually once it catches up ---\n';
        }
      }, 2000);
    }

    function watchReportUpdated(id, baseline, pane) {
      let attempts = 0;
      const maxAttempts = 60;
      const poll = setInterval(async () => {
        attempts++;
        const data = await fetchAgentVersionInfo(id);
        if (data && data.last_seen && data.last_seen !== baseline) {
          clearInterval(poll);
          location.reload();
          return;
        }
        if (attempts >= maxAttempts) {
          clearInterval(poll);
          pane.textContent += '--- still showing pre-apply data after 2 minutes -- reload manually once the fresh report lands ---\n';
        }
      }, 2000);
    }

    async function postApply(id, body) {
      const secret = getAdminApplySecret();
      if (!secret) return;
      const es = openLiveOutput(id);
      try {
        const resp = await fetch('/admin/agents/' + id + '/apply', {
          method: 'POST',
          headers: {'Content-Type': 'application/json', 'X-Admin-Apply-Secret': secret},
          body: JSON.stringify(body),
        });
        if (!resp.ok) {
          closeLiveOutput(id, es);
          if (resp.status === 403) {
            localStorage.removeItem('adminApplySecret');
            alert('apply failed (403): wrong secret -- cleared it, try again with the correct value');
          } else {
            alert('apply failed (' + resp.status + '): ' + await resp.text());
          }
          return;
        }
      } catch (e) {
        closeLiveOutput(id, es);
        alert('apply failed: ' + e);
      }
    }
    function applyAction(id, type) {
      postApply(id, {type: type});
    }
    function applyPackages(event, id) {
      event.preventDefault();
      const packages = Array.from(event.target.querySelectorAll('input[name=pkg]:checked')).map(i => i.value);
      if (packages.length === 0) {
        alert('select at least one package');
        return false;
      }
      postApply(id, {type: 'packages', packages: packages});
      return false;
    }
    async function forceRecheck(id) {
      const es = openLiveOutput(id);
      try {
        const resp = await fetch('/admin/agents/' + id + '/recheck', {method: 'POST'});
        if (!resp.ok) {
          closeLiveOutput(id, es);
          alert('recheck failed (' + resp.status + '): ' + await resp.text());
          return;
        }
      } catch (e) {
        closeLiveOutput(id, es);
        alert('recheck failed: ' + e);
      }
    }

    async function postSelfUpdateChannel(channel) {
      const secret = getAdminApplySecret();
      if (!secret) return;
      try {
        const resp = await fetch('/admin/self-update-channel', {
          method: 'POST',
          headers: {'Content-Type': 'application/json', 'X-Admin-Apply-Secret': secret},
          body: JSON.stringify({channel: channel}),
        });
        if (!resp.ok) {
          if (resp.status === 403) {
            localStorage.removeItem('adminApplySecret');
            alert('channel switch failed (403): wrong secret -- cleared it, try again with the correct value');
          } else {
            alert('channel switch failed (' + resp.status + '): ' + await resp.text());
          }
          return;
        }
        location.reload();
      } catch (e) {
        alert('channel switch failed: ' + e);
      }
    }

    const startingAggregatorVersion = "{{.AggregatorVersion}}";

    async function postSelfUpdate(id, component, targetVersion) {
      const secret = getAdminApplySecret();
      if (!secret) return;
      const expect = component === 'aggregator' ? null : {component: component, targetVersion: targetVersion};
      const es = openLiveOutput(id, expect);
      try {
        const resp = await fetch('/admin/agents/' + id + '/self-update', {
          method: 'POST',
          headers: {'Content-Type': 'application/json', 'X-Admin-Apply-Secret': secret},
          body: JSON.stringify({component: component, target_version: targetVersion}),
        });
        if (!resp.ok) {
          closeLiveOutput(id, es);
          if (resp.status === 403) {
            localStorage.removeItem('adminApplySecret');
            alert('self-update failed (403): wrong secret -- cleared it, try again with the correct value');
          } else {
            alert('self-update failed (' + resp.status + '): ' + await resp.text());
          }
          return;
        }
        if (component === 'aggregator') {
          watchAggregatorRestart();
        }
      } catch (e) {
        closeLiveOutput(id, es);
        alert('self-update failed: ' + e);
      }
    }

    function watchAggregatorRestart() {
      const banner = document.getElementById('restartBanner');
      banner.style.display = 'block';
      let attempts = 0;
      const maxAttempts = 60;
      const poll = setInterval(async () => {
        attempts++;
        try {
          const resp = await fetch('/healthz', {cache: 'no-store'});
          if (resp.ok) {
            const data = await resp.json();
            if (data.version && data.version !== startingAggregatorVersion) {
              clearInterval(poll);
              location.reload();
              return;
            }
          }
        } catch (e) {}
        if (attempts >= maxAttempts) {
          clearInterval(poll);
          banner.textContent = 'Aggregator restart is taking longer than expected -- reload manually once it\'s back.';
        }
      }, 2000);
    }

    document.querySelectorAll('[data-pending-action-id]').forEach((pane) => {
      openLiveOutput(pane.dataset.agentId);
    });
  </script>
</body>
</html>
`
