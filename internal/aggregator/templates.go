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
	// companion or the agent itself (see ClientKind) -- gates the
	// "connected"/Force-recheck display, since recheck works either way.
	// CompanionConnected is strictly kind==companion -- gates apply-only
	// controls, since only a real companion can run apt-get. ConnectedVia
	// is "agent"/"companion"/"" (not connected), for the on-page label.
	AnyStreamConnected bool
	CompanionConnected bool
	ConnectedVia       string
	CompanionVersion   string
	RecentResults      []resultView

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
	// SelfUpdateIncludePreRelease mirrors selfupdate.Client's own current
	// channel (SELF_UPDATE_INCLUDE_PRERELEASE at startup, or whatever an
	// operator has since switched it to via the toggle below).
	SelfUpdateIncludePreRelease bool
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
		ConnectedVia:             string(kind),
		CompanionVersion:         companionVersion,
		CompanionUpdateAvailable: updateAvailable(latestVersion, companionVersion) && !aggregatorBehind,
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
<html>
<head>
  <meta charset="utf-8">
  <title>update-detector aggregator</title>
  <style>
    body { font-family: sans-serif; margin: 2rem; }
    table { border-collapse: collapse; width: 100%; margin-bottom: 2rem; }
    th, td { border: 1px solid #ccc; padding: 0.4rem 0.6rem; text-align: left; }
    form { display: inline; }
    button { cursor: pointer; }
    .ok { color: #1a7f37; }
    .bad { color: #b91c1c; }
    .muted { color: #666; }
    .apply-section { margin-top: 0.5rem; }
    .apply-section button { margin-right: 0.3rem; margin-top: 0.3rem; }
    .banner { background: #fff8e1; border: 1px solid #e0c66b; border-radius: 4px; padding: 0.6rem 1rem; margin-bottom: 1rem; }
    .restart-banner { display: none; position: sticky; top: 0; background: #1a7f37; color: #fff; padding: 0.6rem 1rem; border-radius: 4px; margin-bottom: 1rem; }
    .output-pane { display: none; background: #1e1e1e; color: #d4d4d4; font-family: monospace; font-size: 0.8rem; padding: 0.5rem; margin-top: 0.4rem; max-height: 240px; overflow-y: auto; white-space: pre-wrap; border-radius: 4px; }
  </style>
</head>
<body>
  <div id="restartBanner" class="restart-banner">Updating the aggregator&hellip; this page will reload automatically once it's back.</div>
  <h1>update-detector aggregator <small class="muted">{{.AggregatorVersion}}</small></h1>
  {{if .SelfUpdateConfigured}}
  <p class="muted">
    Self-update channel:
    <label><input type="radio" name="selfUpdateChannel" value="release" {{if not .SelfUpdateIncludePreRelease}}checked{{end}} onchange="postSelfUpdateChannel(false)"> release only</label>
    <label><input type="radio" name="selfUpdateChannel" value="prerelease" {{if .SelfUpdateIncludePreRelease}}checked{{end}} onchange="postSelfUpdateChannel(true)"> include pre-releases (-rcN)</label>
  </p>
  {{end}}
  {{if .AggregatorUpdateAvailable}}
  <div class="banner">
    <strong>{{.LatestVersion}}</strong> is available (this aggregator is running <strong>{{.AggregatorVersion}}</strong>).
    Use "Update aggregator" from whichever host's row below actually runs it -- there's no single button here since
    the aggregator itself isn't one of the per-host rows.
  </div>
  {{end}}
  <p><a href="/widgets/packages">all pending packages (fleet-wide)</a> &middot; <a href="/widgets/packages?security=true">security only</a> &middot; <a href="/widgets/summary">summary</a></p>

  <h2>Pending ({{len .Pending}})</h2>
  <table>
    <tr><th>Hostname</th><th>Agent ID</th><th>First seen</th><th>Actions</th></tr>
    {{range .Pending}}
    <tr>
      <td>{{.Hostname}}</td>
      <td>{{.ShortID}}</td>
      <td>{{.FirstSeen}}</td>
      <td>
        <form method="post" action="/admin/agents/{{.ID}}/approve"><button>Approve</button></form>
        <form method="post" action="/admin/agents/{{.ID}}/reject"><button>Reject</button></form>
      </td>
    </tr>
    {{else}}
    <tr><td colspan="4" class="muted">none</td></tr>
    {{end}}
  </table>

  <h2>Approved ({{len .Approved}})</h2>
  <table>
    <tr><th>Hostname</th><th>Agent ID</th><th>Last seen</th><th>Last report</th><th>Companion</th><th>Actions</th></tr>
    {{range .Approved}}
    <tr>
      <td>{{.Hostname}}</td>
      <td>{{.ShortID}}</td>
      <td>{{.LastSeen}}</td>
      <td>
        {{if .HasReport}}
          {{if .OK}}<span class="ok">OK</span>{{else}}<span class="bad">needs attention</span>{{end}}
          &mdash; {{.UpgradableTotal}} upgradable ({{.UpgradableSecurity}} security){{if .RebootRequired}}, reboot required{{end}}{{if .OSUpdateAvailable}}, OS upgrade available{{end}}
          {{if .AgentVersion}}<br><span class="muted">agent {{.AgentVersion}}</span>{{end}}
          {{if .Upgrades}}
          <details>
            <summary>show packages</summary>
            <ul>
              {{range .Upgrades}}
              <li>{{if .Security}}<strong class="bad">{{.Name}}</strong>{{else}}{{.Name}}{{end}}{{if .CurrentVersion}} {{.CurrentVersion}} &rarr;{{end}} {{.CandidateVersion}}</li>
              {{end}}
            </ul>
          </details>
          {{end}}
          <br><a href="/widgets/hosts/{{.Hostname}}">raw JSON</a>
          {{if .Errors}}<br><span class="muted">errors: {{range .Errors}}{{.}}; {{end}}</span>{{end}}
        {{else}}
          <span class="muted">no report yet</span>
        {{end}}
      </td>
      <td class="apply-section">
        {{if .AnyStreamConnected}}
          <span class="ok">connected{{if eq .ConnectedVia "agent"}} (via agent){{end}}</span>{{if .CompanionVersion}} <span class="muted">{{.CompanionVersion}}</span>{{end}}<br>
          {{if .CompanionConnected}}
            {{if .Upgrades}}
            <details>
              <summary>apply packages</summary>
              <form onsubmit="return applyPackages(event, '{{.ID}}')">
                {{range .Upgrades}}
                <label><input type="checkbox" name="pkg" value="{{.Name}}"> {{.Name}}</label><br>
                {{end}}
                <button type="submit">Apply selected</button>
              </form>
            </details>
            {{end}}
            <button onclick="applyAction('{{.ID}}', 'upgrade')">Upgrade all</button>
            <button onclick="applyAction('{{.ID}}', 'full-upgrade')">Full upgrade all</button>
            {{if .AgentUpdateAvailable}}
            <button onclick="postSelfUpdate('{{.ID}}', 'agent', '{{$.LatestVersion}}')" title="Update this host's agent to {{$.LatestVersion}}">Update agent</button>
            {{end}}
            {{if $.AggregatorUpdateAvailable}}
            <button onclick="postSelfUpdate('{{.ID}}', 'aggregator', '{{$.LatestVersion}}')" title="Update the aggregator co-located with this host, if any, to {{$.LatestVersion}}">Update aggregator</button>
            {{end}}
            {{if .CompanionUpdateAvailable}}
            <button onclick="postSelfUpdate('{{.ID}}', 'companion', '{{$.LatestVersion}}')" title="Update this host's companion to {{$.LatestVersion}}">Update companion</button>
            {{end}}
          {{else}}
            <span class="muted">install companion to enable apply</span><br>
          {{end}}
          <button onclick="forceRecheck('{{.ID}}')" title="Re-scan this host now instead of waiting for the next CHECK_INTERVAL">Force recheck</button>
        {{else}}
          <span class="muted">not connected</span>
        {{end}}
        <pre id="output-{{.ID}}" class="output-pane" data-agent-id="{{.ID}}"{{if .PendingActionID}} data-pending-action-id="{{.PendingActionID}}"{{end}}></pre>
        {{if .RecentResults}}
        <details>
          <summary>recent actions ({{len .RecentResults}})</summary>
          <ul>
            {{range .RecentResults}}
            <li>{{.CompletedAt}} &mdash; {{if .Success}}<span class="ok">success</span>{{else}}<span class="bad">failed</span>{{end}}: {{.Message}}</li>
            {{end}}
          </ul>
        </details>
        {{end}}
      </td>
      <td>
        <form method="post" action="/admin/agents/{{.ID}}/reject"><button>Revoke</button></form>
      </td>
    </tr>
    {{else}}
    <tr><td colspan="6" class="muted">none</td></tr>
    {{end}}
  </table>

  <h2>Rejected ({{len .Rejected}})</h2>
  <table>
    <tr><th>Hostname</th><th>Agent ID</th><th>Actions</th></tr>
    {{range .Rejected}}
    <tr>
      <td>{{.Hostname}}</td>
      <td>{{.ShortID}}</td>
      <td>
        <form method="post" action="/admin/agents/{{.ID}}/approve"><button>Approve</button></form>
      </td>
    </tr>
    {{else}}
    <tr><td colspan="3" class="muted">none</td></tr>
    {{end}}
  </table>

  <script>
    // No reverse-proxy needed to use this: the browser asks for
    // ADMIN_APPLY_SHARED_SECRET once, remembers it in this browser's
    // localStorage, and sends it as X-Admin-Apply-Secret on every apply
    // call -- the exact same header the server already checks either way.
    // If you *do* put the aggregator behind your own auth (e.g.
    // Authentik) and have it inject this header itself instead, that
    // works too and this prompt just never fires (the stored value is
    // only used as a fallback when the header isn't already present).
    function getAdminApplySecret() {
      let secret = localStorage.getItem('adminApplySecret');
      if (!secret) {
        secret = prompt('Enter the aggregator\'s ADMIN_APPLY_SHARED_SECRET (see README):');
        if (secret) localStorage.setItem('adminApplySecret', secret);
      }
      return secret;
    }

    // Opens a live view of actionID's output on id's row, replacing
    // whatever this row's pane was showing before. Multiple rows' streams
    // are fully independent EventSource connections -- this is what lets
    // several hosts update concurrently, each with its own live view, on
    // the same page load.
    //
    // selfUpdateExpect is only set for a self-update of "agent" or
    // "companion" -- {component, targetVersion}. For anything else
    // (package apply/upgrade/full-upgrade/recheck, or a self-update of
    // "aggregator", which watchAggregatorRestart already watches
    // separately), null.
    function openLiveOutput(id, actionId, selfUpdateExpect) {
      if (!actionId) return;
      const pane = document.getElementById('output-' + id);
      if (!pane) return;
      pane.style.display = 'block';
      pane.textContent = '';

      // Captured now, before the action has had any realistic chance to
      // produce a fresh report (the companion hasn't even started running
      // apt-get yet) -- the baseline watchReportUpdated compares against
      // once "done" fires, for the non-self-update case below.
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
          // "done" here only means the companion's restart/redeploy
          // command was *issued* successfully -- the row's version
          // label won't actually reflect it until the new agent/companion
          // process finishes starting up and reports back in on its own,
          // which routinely takes longer than any fixed short delay would
          // assume. Poll for that report specifically instead of guessing.
          pane.textContent += '--- done -- waiting for ' + selfUpdateExpect.component +
            ' to report version ' + selfUpdateExpect.targetVersion + ' ---\n';
          watchComponentVersion(id, selfUpdateExpect, pane);
        } else {
          // "done" here means the companion finished apt-get and (for
          // anything but a plain recheck) triggered the agent's own
          // out-of-band recheck -- but that recheck is itself another
          // detection cycle plus a report round-trip to this aggregator,
          // which routinely takes longer than any fixed short delay would
          // assume. Without waiting for it, this row's upgradable-package
          // list/counts (only ever computed at page-render time) reload
          // showing the pre-apply numbers, exactly as if nothing happened,
          // until a later manual refresh happens to land after the real
          // report. Poll for that report specifically instead of guessing.
          pane.textContent += '--- done -- waiting for the fresh report to land ---\n';
          watchReportUpdated(id, await baselinePromise, pane);
        }
      });
      es.addEventListener('disconnected', () => {
        pane.textContent += '--- companion disconnected -- waiting for it to come back ---\n';
        es.close();
      });
    }

    // Best-effort fetch of GET /admin/agents/{id}/version -- straight from
    // the registry/hub, not the page's own stale render. Returns null (not
    // a throw) on any failure, so callers can just treat that as "try
    // again next poll" instead of every call site handling its own catch.
    async function fetchAgentVersionInfo(id) {
      try {
        const resp = await fetch('/admin/agents/' + id + '/version', {cache: 'no-store'});
        if (resp.ok) return await resp.json();
      } catch (e) {
        // Transient fetch failure -- treated the same as a non-OK
        // response by every caller below: keep polling.
      }
      return null;
    }

    // Polls until expect.component's reported version actually matches
    // expect.targetVersion, then reloads. Same "poll instead of assume"
    // pattern as watchAggregatorRestart. Gives up after maxAttempts and
    // leaves a message instead of ever reloading onto what might still be
    // a stale render.
    function watchComponentVersion(id, expect, pane) {
      const field = expect.component === 'companion' ? 'companion_version' : 'agent_version';
      let attempts = 0;
      const maxAttempts = 60; // ~2 minutes at 2s each
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

    // Polls until this host's last-report time actually advances past
    // baseline (captured just before the action started), then reloads --
    // baseline, not a specific expected value, since a plain apply has no
    // single "target" to compare against the way a self-update's version
    // does; any strictly newer report reflects this action's outcome.
    // Gives up after maxAttempts and leaves a message instead of ever
    // reloading onto what might still be pre-apply data.
    function watchReportUpdated(id, baseline, pane) {
      let attempts = 0;
      const maxAttempts = 60; // ~2 minutes at 2s each
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
      try {
        const resp = await fetch('/admin/agents/' + id + '/apply', {
          method: 'POST',
          headers: {'Content-Type': 'application/json', 'X-Admin-Apply-Secret': secret},
          body: JSON.stringify(body),
        });
        if (!resp.ok) {
          if (resp.status === 403) {
            localStorage.removeItem('adminApplySecret');
            alert('apply failed (403): wrong secret -- cleared it, try again with the correct value');
          } else {
            alert('apply failed (' + resp.status + '): ' + await resp.text());
          }
          return;
        }
        const data = await resp.json();
        openLiveOutput(id, data.action_id);
      } catch (e) {
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
    // No secret needed here, unlike postApply -- recheck can't change
    // anything on the host, only make it report sooner.
    async function forceRecheck(id) {
      try {
        const resp = await fetch('/admin/agents/' + id + '/recheck', {method: 'POST'});
        if (!resp.ok) {
          alert('recheck failed (' + resp.status + '): ' + await resp.text());
          return;
        }
        alert('recheck triggered -- reload this page in a bit to see updated data');
      } catch (e) {
        alert('recheck failed: ' + e);
      }
    }

    async function postSelfUpdateChannel(includePreRelease) {
      const secret = getAdminApplySecret();
      if (!secret) return;
      try {
        const resp = await fetch('/admin/self-update-channel', {
          method: 'POST',
          headers: {'Content-Type': 'application/json', 'X-Admin-Apply-Secret': secret},
          body: JSON.stringify({include_prerelease: includePreRelease}),
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

    // This page's own aggregator version at load time -- html/template
    // context-escapes this correctly for a JS string literal, same as
    // every other templated value elsewhere on this page.
    const startingAggregatorVersion = "{{.AggregatorVersion}}";

    async function postSelfUpdate(id, component, targetVersion) {
      const secret = getAdminApplySecret();
      if (!secret) return;
      try {
        const resp = await fetch('/admin/agents/' + id + '/self-update', {
          method: 'POST',
          headers: {'Content-Type': 'application/json', 'X-Admin-Apply-Secret': secret},
          body: JSON.stringify({component: component, target_version: targetVersion}),
        });
        if (!resp.ok) {
          if (resp.status === 403) {
            localStorage.removeItem('adminApplySecret');
            alert('self-update failed (403): wrong secret -- cleared it, try again with the correct value');
          } else {
            alert('self-update failed (' + resp.status + '): ' + await resp.text());
          }
          return;
        }
        const data = await resp.json();
        // "aggregator" has no version-polling expectation here -- the
        // page itself is about to go down and watchAggregatorRestart
        // below is what actually detects it coming back up on the new
        // version; agent/companion have no such signal, so they poll
        // /admin/agents/{id}/version instead of guessing a delay.
        const expect = component === 'aggregator' ? null : {component: component, targetVersion: targetVersion};
        openLiveOutput(id, data.action_id, expect);
        // Updating the aggregator additionally takes down this page's own
        // process -- watch for it coming back on top of (not instead of)
        // the live view above, since the live view itself will just show
        // "disconnected" once the restart actually happens.
        if (component === 'aggregator') {
          watchAggregatorRestart();
        }
      } catch (e) {
        alert('self-update failed: ' + e);
      }
    }

    // Jenkins-style: show a banner, keep polling /healthz in the
    // background through the restart window (fetch failures are
    // expected and ignored while the aggregator is actually down), and
    // reload only once it reports a version that's actually different
    // from what was running when this update was triggered -- not just
    // "got any response," since a stale response during the restart
    // window shouldn't look like success.
    function watchAggregatorRestart() {
      const banner = document.getElementById('restartBanner');
      banner.style.display = 'block';
      let attempts = 0;
      const maxAttempts = 60; // ~2 minutes at 2s each
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
        } catch (e) {
          // Expected while the aggregator is actually down mid-restart --
          // keep polling rather than treating this as a failure.
        }
        if (attempts >= maxAttempts) {
          clearInterval(poll);
          banner.textContent = 'Aggregator restart is taking longer than expected -- reload manually once it\'s back.';
        }
      }, 2000);
    }

    // Resume watching any action already in flight when this page loads
    // -- e.g. triggered from another tab, by another operator, or before
    // a reload -- not just ones this exact page load itself triggers.
    document.querySelectorAll('[data-pending-action-id]').forEach((pane) => {
      openLiveOutput(pane.dataset.agentId, pane.dataset.pendingActionId);
    });
  </script>
</body>
</html>
`
