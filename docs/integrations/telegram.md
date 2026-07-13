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

Back to [README](../../README.md).
