package aggregator

import (
	"html/template"
	"io"
	"time"
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
	RebootRequired     bool
	OSUpdateAvailable  bool
}

type adminPageData struct {
	Pending  []agentView
	Approved []agentView
	Rejected []agentView
}

func toAgentView(rec AgentRecord) agentView {
	v := agentView{
		ID:       rec.ID,
		ShortID:  shortID(rec.ID),
		Hostname: rec.Hostname,
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
		v.RebootRequired = rec.LastReport.RebootRequired
		v.OSUpdateAvailable = rec.LastReport.OS.UpdateAvailable
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
  </style>
</head>
<body>
  <h1>update-detector aggregator</h1>

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
    <tr><th>Hostname</th><th>Agent ID</th><th>Last seen</th><th>Last report</th><th>Actions</th></tr>
    {{range .Approved}}
    <tr>
      <td>{{.Hostname}}</td>
      <td>{{.ShortID}}</td>
      <td>{{.LastSeen}}</td>
      <td>
        {{if .HasReport}}
          {{if .OK}}<span class="ok">OK</span>{{else}}<span class="bad">needs attention</span>{{end}}
          &mdash; {{.UpgradableTotal}} upgradable ({{.UpgradableSecurity}} security){{if .RebootRequired}}, reboot required{{end}}{{if .OSUpdateAvailable}}, OS upgrade available{{end}}
          {{if .Errors}}<br><span class="muted">errors: {{range .Errors}}{{.}}; {{end}}</span>{{end}}
        {{else}}
          <span class="muted">no report yet</span>
        {{end}}
      </td>
      <td>
        <form method="post" action="/admin/agents/{{.ID}}/reject"><button>Revoke</button></form>
      </td>
    </tr>
    {{else}}
    <tr><td colspan="5" class="muted">none</td></tr>
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
</body>
</html>
`
