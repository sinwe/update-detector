package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
)

const telegramAPIBase = "https://api.telegram.org"

// Telegram sends notifications via the Telegram Bot API's sendMessage
// endpoint. Create a bot with @BotFather, add it to the target chat, and
// supply its token and the chat ID via TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID.
type Telegram struct {
	botToken string
	chatID   string
	client   *http.Client
	apiBase  string
}

func NewTelegram(botToken, chatID string) *Telegram {
	return &Telegram{
		botToken: botToken,
		chatID:   chatID,
		client:   &http.Client{Timeout: 10 * time.Second},
		apiBase:  telegramAPIBase,
	}
}

func (t *Telegram) Name() string { return "telegram" }

func (t *Telegram) Send(ctx context.Context, ev Event) error {
	payload, err := json.Marshal(map[string]string{
		"chat_id":    t.chatID,
		"text":       FormatMessage(ev),
		"parse_mode": "HTML",
	})
	if err != nil {
		return fmt.Errorf("telegram: encoding payload: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", t.apiBase, t.botToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("telegram: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram: unexpected status %s", resp.Status)
	}
	return nil
}

// FormatMessage renders a human-readable HTML notification for an Event.
func FormatMessage(ev Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>: update status changed\n", html.EscapeString(ev.Hostname))
	for _, c := range ev.Changes {
		fmt.Fprintf(&b, "• %s\n", html.EscapeString(c))
	}

	s := ev.Status
	fmt.Fprintf(&b, "\nUpgradable: %d (security: %d)\n", s.Packages.UpgradableTotal, s.Packages.UpgradableSecurity)
	fmt.Fprintf(&b, "Reboot required: %v\n", s.RebootRequired)
	if s.OS.UpdateAvailable {
		fmt.Fprintf(&b, "OS upgrade available: %s -> %s\n",
			html.EscapeString(s.OS.CurrentVersion), html.EscapeString(s.OS.LatestVersion))
	}
	return b.String()
}
