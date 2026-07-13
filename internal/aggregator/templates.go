package aggregator

import (
	"html/template"
	"io"
	"time"

	"update-detector/internal/checker"
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

	// CompanionConnected, CompanionVersion, and RecentResults are only
	// meaningful once Approved -- a companion has nothing to connect to,
	// or report on, before that.
	CompanionConnected bool
	CompanionVersion   string
	RecentResults      []resultView
}

type resultView struct {
	Success     bool
	Message     string
	CompletedAt string
}

type adminPageData struct {
	AggregatorVersion string
	Pending           []agentView
	Approved          []agentView
	Rejected          []agentView
}

func toAgentView(rec AgentRecord, hub *CompanionHub) agentView {
	v := agentView{
		ID:                 rec.ID,
		ShortID:            shortID(rec.ID),
		Hostname:           rec.Hostname,
		CompanionConnected: hub.Connected(rec.ID),
		CompanionVersion:   hub.CompanionVersion(rec.ID),
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
  </style>
</head>
<body>
  <h1>update-detector aggregator <small class="muted">{{.AggregatorVersion}}</small></h1>
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
        {{if .CompanionConnected}}
          <span class="ok">connected</span>{{if .CompanionVersion}} <span class="muted">{{.CompanionVersion}}</span>{{end}}<br>
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
          <button onclick="forceRecheck('{{.ID}}')" title="Re-scan this host now instead of waiting for the next CHECK_INTERVAL">Force recheck</button>
        {{else}}
          <span class="muted">not connected</span>
        {{end}}
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
        alert('action accepted -- reload this page in a bit to see the result');
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
  </script>
</body>
</html>
`
