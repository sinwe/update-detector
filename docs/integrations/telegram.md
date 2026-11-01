# Telegram notifications

Wired in when both `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID` are set:

1. Create a bot with [@BotFather](https://t.me/BotFather), grab its token.
2. Add the bot to your chat/channel and find the chat ID (e.g. via
   `https://api.telegram.org/bot<token>/getUpdates` after sending it a
   message).
3. Set the two env vars in `docker-compose.yml` or your environment.

A notification is sent only on a meaningful state transition — new updates
appear, the security count increases, reboot flips from not-required to
required, or an OS upgrade newly becomes available — using the persisted
state file (`STATE_FILE`) as the baseline, so a container restart doesn't
re-send the same alert.

The aggregator has its own, independent `TELEGRAM_BOT_TOKEN`/`TELEGRAM_CHAT_ID`
pair (see [docs/integrations/homepage.md](homepage.md)) for alerting on
companion apply results — set it separately if you want that too.

## Per-host bots (aggregator `Notification` page)

The env-var setup above is one bot for the whole fleet. If hosts belong to
different people — a shared lab, or a few machines lent to family — each
owner wants their own chat, and nobody wants to hold the aggregator's
admin token. `/admin/notifications` (linked as **🔔 Notifications** from
the dashboard) gives every approved host its own bot token + chat ID.

Per host, the aggregator picks exactly one delivery path:

| Host has… | Update alerts | Offline/recovery alerts |
|---|---|---|
| an active own bot | that bot's chat (aggregator sends it) | that bot's chat |
| a paused/cleared config | whatever the host's own agent does (nothing, unless the agent has its own env pair) | fleet-wide `TELEGRAM_CHAT_ID`, if set |
| no config at all | same as above | fleet-wide `TELEGRAM_CHAT_ID`, if set |

So the aggregator only *sends* update alerts itself for hosts with an
active own bot — it never duplicates an agent-side notification, and a
host that is silent today (agent env pair unset, which is why a
freshly-enrolled host can sit on pending updates with no message) starts
notifying the moment you configure it here, with nothing to change on the
host.

**Getting the two values.** They come from two different people, on
purpose:

- **Bot token — aggregator admin.** Open
  [@BotFather](https://t.me/BotFather), send `/newbot`, pick a name and a
  username ending in `bot`. The reply contains
  `123456789:AAH4xQ...`; that whole string is the token. `/revoke` in the
  same chat invalidates it.
- **Chat ID — the host's own user.** They open a chat with that bot and
  press **Start** (a bot cannot message someone who never started it),
  then load
  `https://api.telegram.org/bot<token>/getUpdates` and read
  `result[0].message.chat.id`. For a group or channel, add the bot to it
  and post a message there first: the ID is then negative
  (`-1001234567890`). DMing [@userinfobot](https://t.me/userinfobot) also
  reports a personal user ID, which works as a chat ID for DMs.

Paste both into that host's card, tick the box, press **Send test** — the
message goes out immediately and a failure (wrong token, bot never
started, wrong chat) comes back as Telegram's own error instead of
silently doing nothing until the next update. **Save** persists it;
**Clear** forgets it and returns the host to the fleet-wide bot. Leaving
the token field blank on save keeps the stored token, since the page only
ever shows a mask (`8891668888:…w2lE`) — that way an owner can fix a chat
ID without being handed the secret again.

Notes:

- Config is keyed by agent ID, not hostname: renaming a host keeps its
  notification target.
- Tokens must stay readable to be usable, so they live in `notify.json`
  next to the registry, mode `0600`, and are never echoed back in full.
  Like the rest of `/admin` the page has no login — anyone who can reach
  it can repoint a host's alerts, so keep it on a trusted network or
  behind your reverse-proxy auth.
- If the *agent* on that host also has `TELEGRAM_BOT_TOKEN` pointing at
  the same chat, you get two messages for one change. Either drop the
  agent's env pair (the aggregator now covers it) or give the agent a
  different chat.
- Apply buttons are unaffected: they still ride the fleet-wide bot the
  aggregator long-polls, because Telegram delivers each press to only one
  `getUpdates` caller. A per-host bot is for delivery only, so an owner's
  bot never needs to be polled by the aggregator.

## Applying updates from the notification

When the agent also has an aggregator configured (`AGGREGATOR_URL`), its
update notifications carry an **Apply all updates** button. Pressing it
runs the same thing as the dashboard's *Upgrade all* button on that one
host only — `apt-get upgrade` on Linux, `brew upgrade` on macOS. Applying
selected packages, a full upgrade (which may remove packages), and OS
version upgrades stay dashboard-only for now.

The press is not handled by the host that sent the message: the
aggregator is the only process that long-polls the bot, because Telegram
delivers each update to exactly one `getUpdates` caller. So:

- The aggregator needs the *same* `TELEGRAM_BOT_TOKEN`/`TELEGRAM_CHAT_ID`
  as the agents, plus `ADMIN_APPLY_SHARED_SECRET`. That secret is what
  gates a real upgrade, so without it the aggregator logs that apply
  buttons are disabled and pressing one does nothing.
- Never point a second aggregator (or anything else) at the same bot
  token: the pollers would fight over presses.
- In a group chat, set `TELEGRAM_APPLY_USER_IDS` to the numeric Telegram
  user IDs allowed to press the button; otherwise any member of the chat
  can.
- Set `TELEGRAM_APPLY_BUTTONS=false` on the agent to stop attaching the
  button, or on the aggregator to stop listening for presses.

A failure (host offline, no connected companion, another apply already
in flight) comes back as an alert on the press, and the button stays in
place so it can be retried. On success the keyboard is cleared, and the
outcome arrives as the usual separate "update applied on <host>"
message once the companion finishes.

Back to [README](../../README.md).
