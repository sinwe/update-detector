package aggregator

import (
	"html/template"
	"io"
)

// notifyTemplate is the standalone /admin/notifications page. Kept apart
// from adminTemplateSrc (rather than as another section on the main page)
// because it holds credentials and needs room for the "how do I get these
// two values" instructions, which would bury the host list otherwise.
var notifyTemplate = template.Must(template.New("notifications").Parse(notifyTemplateSrc))

func renderNotifyPage(w io.Writer, data notifyPageData) error {
	return notifyTemplate.Execute(w, data)
}

const notifyTemplateSrc = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Notifications — Update Detector</title>
  <style>
    :root {
      --bg: #f8f9fa; --surface: #fff; --border: #e9ecef; --border-strong: #dee2e6;
      --text: #212529; --text-secondary: #868e96; --text-muted: #adb5bd;
      --green: #40c057; --green-bg: #d3f9d8; --green-text: #2b8a3e;
      --red: #fa5252; --red-bg: #ffe3e3; --red-text: #c92a2a;
      --orange: #fd7e14; --orange-bg: #fff4e6; --orange-text: #d9480f;
      --blue: #4c6ef5; --blue-bg: #dbe4ff; --blue-text: #364fc7;
      --radius: 10px; --radius-sm: 6px;
      --shadow: 0 1px 3px rgba(0,0,0,.06), 0 1px 2px rgba(0,0,0,.04);
    }
    @media(prefers-color-scheme:dark) {
      :root {
        --bg: #1a1b1e; --surface: #25262b; --border: #2c2e33; --border-strong: #373a40;
        --text: #e9ecef; --text-secondary: #a6a7ab; --text-muted: #6c6f75;
        --green-bg: #1c2b1e; --green-text: #69db7c;
        --red-bg: #2b1a1a; --red-text: #ff8787;
        --orange-bg: #2b221a; --orange-text: #ffa94d;
        --blue-bg: #1a2233; --blue-text: #74c0fc;
      }
    }
    * { box-sizing: border-box; }
    body {
      margin: 0; padding: 1.5rem 1rem 3rem; background: var(--bg); color: var(--text);
      font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
      font-size: 14px; line-height: 1.5;
    }
    .container { max-width: 880px; margin: 0 auto; }
    .header { display: flex; align-items: baseline; justify-content: space-between; gap: 1rem; flex-wrap: wrap; margin-bottom: 1.25rem; }
    h1 { font-size: 1.25rem; margin: 0; display: flex; align-items: center; gap: .5rem; }
    .version-badge {
      font-size: .688rem; font-weight: 500; color: var(--text-secondary);
      background: var(--surface); border: 1px solid var(--border);
      padding: .1rem .4rem; border-radius: 999px;
    }
    a { color: var(--blue-text); }
    .banner { padding: .6rem .9rem; border-radius: var(--radius-sm); margin-bottom: 1rem; font-size: .875rem; }
    .banner-ok { background: var(--green-bg); color: var(--green-text); }
    .banner-err { background: var(--red-bg); color: var(--red-text); }

    .help {
      background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius);
      padding: 1rem 1.25rem; margin-bottom: 1.5rem; box-shadow: var(--shadow);
    }
    .help h2 { font-size: .938rem; margin: 0 0 .5rem; }
    .help h3 { font-size: .813rem; margin: 1rem 0 .35rem; color: var(--text-secondary); text-transform: uppercase; letter-spacing: .03em; }
    .help p, .help li { font-size: .875rem; color: var(--text); }
    .help ol { margin: .25rem 0 .25rem 1.1rem; padding: 0; }
    .help li { margin-bottom: .3rem; }
    .help code, .help pre {
      font-family: 'SF Mono', 'Fira Code', monospace; font-size: .78rem;
      background: var(--bg); border: 1px solid var(--border); border-radius: var(--radius-sm);
    }
    .help code { padding: .05rem .3rem; }
    .help pre { padding: .6rem .75rem; overflow-x: auto; margin: .4rem 0; }
    .help .note { color: var(--text-secondary); font-size: .813rem; }

    .section { margin-bottom: 1.5rem; }
    .section-head { display: flex; align-items: center; gap: .5rem; margin-bottom: .6rem; }
    .section-title { font-size: .938rem; font-weight: 600; }
    .section-count {
      font-size: .75rem; color: var(--text-secondary); background: var(--surface);
      border: 1px solid var(--border); padding: .05rem .45rem; border-radius: 999px;
    }

    .host-card {
      background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius);
      padding: .85rem 1rem; margin-bottom: .6rem; box-shadow: var(--shadow);
    }
    .host-head { display: flex; align-items: center; gap: .5rem; flex-wrap: wrap; margin-bottom: .6rem; }
    .host-name { font-weight: 600; font-size: .938rem; }
    .host-id { font-size: .75rem; color: var(--text-muted); font-family: 'SF Mono', monospace; }
    .badge { font-size: .688rem; font-weight: 600; padding: .1rem .45rem; border-radius: 999px; }
    .badge-ok { background: var(--green-bg); color: var(--green-text); }
    .badge-warn { background: var(--orange-bg); color: var(--orange-text); }
    .badge-muted { background: var(--bg); color: var(--text-secondary); border: 1px solid var(--border); }

    .fields { display: grid; grid-template-columns: 1fr 1fr; gap: .6rem .75rem; margin-bottom: .6rem; }
    .fields label { display: flex; flex-direction: column; gap: .2rem; font-size: .75rem; color: var(--text-secondary); }
    .fields input[type=text], .fields input[type=password] {
      font-family: 'SF Mono', monospace; font-size: .813rem; padding: .35rem .5rem;
      border: 1px solid var(--border-strong); border-radius: var(--radius-sm);
      background: var(--bg); color: var(--text); width: 100%;
    }
    .fields .check {
      grid-column: 1 / -1; flex-direction: row; align-items: center; gap: .4rem;
      font-size: .813rem; color: var(--text);
    }
    .actions { display: flex; align-items: center; gap: .4rem; flex-wrap: wrap; }
    button {
      font: inherit; font-size: .813rem; font-weight: 500; cursor: pointer;
      padding: .35rem .75rem; border-radius: var(--radius-sm); border: 1px solid transparent;
    }
    .btn-primary { background: var(--blue); color: #fff; }
    .btn-warn { background: var(--orange-bg); color: var(--orange-text); border-color: var(--orange); }
    .btn-danger { background: transparent; color: var(--red-text); border-color: var(--border-strong); }
    .muted { color: var(--text-muted); font-size: .75rem; margin-left: auto; }
    @media(max-width: 620px) { .fields { grid-template-columns: 1fr; } }
  </style>
</head>
<body>
  <div class="container">
    <div class="header">
      <h1>🔔 Notifications <span class="version-badge">{{.AggregatorVersion}}</span></h1>
      <a href="/admin">&larr; Back to hosts</a>
    </div>

    {{if .Message}}<div class="banner banner-ok">{{.Message}}</div>{{end}}
    {{if .Error}}<div class="banner banner-err">{{.Error}}</div>{{end}}

    <div class="help">
      <h2>Per-host Telegram alerts</h2>
      <p>
        Give a host its own bot and it sends its update and offline alerts to
        that bot's chat instead of the aggregator-wide one. Hosts without their
        own bot keep using the fleet-wide <code>TELEGRAM_BOT_TOKEN</code> /
        <code>TELEGRAM_CHAT_ID</code> pair{{if not .FleetBotConfigured}} (currently
        not configured, so those hosts send nothing){{end}} — each host is
        delivered to exactly one place, never both.
      </p>

      <h3>1. Create the bot — done by the aggregator admin</h3>
      <ol>
        <li>Open <a href="https://t.me/BotFather" target="_blank" rel="noopener">@BotFather</a> in Telegram and send <code>/newbot</code>.</li>
        <li>Pick a name and a username (must end in <code>bot</code>).</li>
        <li>BotFather replies with a token like <code>123456789:AAH4xQ...</code>. That is the <strong>bot token</strong>.</li>
      </ol>
      <p class="note">Treat it like a password: anyone holding it can send as that bot, and it can be revoked with <code>/revoke</code>.</p>

      <h3>2. Find the chat ID — done by the host's own user</h3>
      <ol>
        <li>Open a chat with the bot (search its username) and press <strong>Start</strong>, or send it any message.</li>
        <li>Open this URL in a browser, replacing the token:
          <pre>https://api.telegram.org/bot&lt;TOKEN&gt;/getUpdates</pre>
        </li>
        <li>In the JSON, read <code>result[0].message.chat.id</code> — that number is the <strong>chat ID</strong>.</li>
      </ol>
      <p class="note">
        For a group or channel, add the bot to it first and post a message there;
        its ID is negative (e.g. <code>-1001234567890</code>). A bot cannot
        message someone who has never started it, so the user must press Start
        once. Sending a message to <code>@userinfobot</code> also reports your
        own user ID, which works as a chat ID for DMs.
      </p>

      <h3>3. Save and test</h3>
      <p class="note">
        Paste both values below, tick the box, then press <strong>Send test</strong> —
        it saves them first, then delivers immediately and reports Telegram's own
        error if something is wrong, without waiting for a real update event. Nothing
        is lost if you test before pressing <strong>Save</strong>.
      </p>

      <h3>Security</h3>
      <p class="note">
        Tokens are stored on the aggregator in a <code>0600</code> file and never
        shown back in full. Like the rest of <code>/admin</code>, this page has no
        login of its own — anyone who can reach it can repoint a host's alerts, so
        keep it on a trusted network or behind your reverse-proxy auth.
      </p>
    </div>

    <div class="section">
      <div class="section-head">
        <span class="section-title">Hosts</span>
        <span class="section-count">{{len .Hosts}}</span>
      </div>
      {{if not .Hosts}}
      <p class="muted">No approved hosts yet — approve a host on the <a href="/admin">main page</a> first.</p>
      {{end}}
      {{range .Hosts}}
      <form class="host-card" method="post" action="/admin/notifications">
        <input type="hidden" name="agent_id" value="{{.ID}}">
        <div class="host-head">
          <span class="host-name">{{.Hostname}}</span>
          {{if .Active}}<span class="badge badge-ok">own bot</span>
          {{else if .Configured}}<span class="badge badge-warn">paused</span>
          {{else}}<span class="badge badge-muted">fleet default</span>{{end}}
          {{if .RemoteAddr}}<span class="host-id">{{.RemoteAddr}}</span>{{end}}
        </div>
        <div class="fields">
          <label>Bot token
            <input type="password" name="bot_token" autocomplete="off"
                   placeholder="{{if .TokenMasked}}{{.TokenMasked}} — leave blank to keep{{else}}123456789:AAH4xQ...{{end}}">
          </label>
          <label>Chat ID
            <input type="text" name="chat_id" value="{{.ChatID}}" placeholder="-1001234567890">
          </label>
          <label class="check">
            <input type="checkbox" name="enabled" {{if or .Active (not .Configured)}}checked{{end}}>
            Send this host's alerts to its own bot
          </label>
        </div>
        <div class="actions">
          <button class="btn-primary" type="submit" name="action" value="save">Save</button>
          <button class="btn-warn" type="submit" name="action" value="test">Send test</button>
          <button class="btn-danger" type="submit" name="action" value="clear">Clear</button>
          {{if .UpdatedAt}}<span class="muted">updated {{.UpdatedAt}}</span>{{end}}
        </div>
      </form>
      {{end}}
    </div>
  </div>
</body>
</html>
`
