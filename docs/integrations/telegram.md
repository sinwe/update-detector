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
