// Package telegrambot receives Telegram inline-button presses for the
// aggregator's "apply all updates" action.
//
// Sending notifications is internal/notifier's job; this package only
// long-polls getUpdates for callback_query updates. It deliberately lives
// in the aggregator (one process for the whole fleet), never in an agent:
// Telegram hands each update to exactly one getUpdates caller, so two
// agents sharing a bot token would either conflict (409) or steal each
// other's button presses. Agents only call sendMessage, which is safe to
// do concurrently with the aggregator's polling.
package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const telegramAPIBase = "https://api.telegram.org"

// applyAllPrefix is the callback_data prefix shared with the sender
// (internal/notifier's Telegram notifier, via ApplyAllCallbackData).
// Keep it short: Telegram caps callback_data at 64 bytes, and an agent ID
// is 32 hex characters.
const applyAllPrefix = "apply_all:"

// ApplyAllCallbackData returns the callback_data for a host's
// "apply all updates" button.
func ApplyAllCallbackData(agentID string) string { return applyAllPrefix + agentID }

// parseApplyAll reverses ApplyAllCallbackData.
func parseApplyAll(data string) (string, bool) {
	id, ok := strings.CutPrefix(data, applyAllPrefix)
	if !ok || id == "" || len(id) > 64 {
		return "", false
	}
	return id, true
}

// ApplyFunc carries out an "apply all updates" request for one host and
// returns its display name for the operator-facing confirmation toast.
// A non-nil error is shown in an alert popup and leaves the button in
// place, so a failure can simply be retried.
type ApplyFunc func(ctx context.Context, agentID string) (hostname string, err error)

// Bot long-polls one bot token for callback_query updates.
type Bot struct {
	token        string
	chatID       string
	allowedUsers map[int64]struct{}
	client       *http.Client
	apiBase      string
}

// New returns a Bot that only honours presses from chatID. An empty
// allowedUserIDs accepts any member of that chat (fine for a private
// chat, risky in a group); when non-empty, the pressing Telegram user
// must also be listed -- see TELEGRAM_APPLY_USER_IDS.
func New(token, chatID string, allowedUserIDs []int64) *Bot {
	allowed := make(map[int64]struct{}, len(allowedUserIDs))
	for _, id := range allowedUserIDs {
		allowed[id] = struct{}{}
	}
	return &Bot{
		token:        token,
		chatID:       strings.TrimSpace(chatID),
		allowedUsers: allowed,
		// getUpdates itself may hold the request open for `timeout`
		// seconds, so the HTTP deadline has to sit above that.
		client:  &http.Client{Timeout: 50 * time.Second},
		apiBase: telegramAPIBase,
	}
}

// Run polls until ctx is cancelled. It never returns an error: a transient
// Telegram outage is logged and retried, because a missed button press is
// not worth killing the aggregator over.
func (b *Bot) Run(ctx context.Context, apply ApplyFunc) {
	log.Printf("telegrambot: listening for apply buttons")
	var offset int64
	for ctx.Err() == nil {
		updates, err := b.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("telegrambot: getUpdates: %v (retrying in 10s)", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			if u.CallbackQuery != nil {
				b.handleCallback(ctx, u.CallbackQuery, apply)
			}
		}
	}
}

func (b *Bot) handleCallback(ctx context.Context, cq *callbackQuery, apply ApplyFunc) {
	// Always answer: Telegram keeps spinner UI on the button until an
	// answerCallbackQuery arrives, and retries the update otherwise.
	if cq.Message == nil || !b.authorized(cq) {
		b.answer(ctx, cq.ID, "Not authorized.", true)
		return
	}

	agentID, ok := parseApplyAll(cq.Data)
	if !ok {
		b.answer(ctx, cq.ID, "Unknown action.", true)
		return
	}

	hostname, err := apply(ctx, agentID)
	if err != nil {
		log.Printf("telegrambot: apply all on %s:%s: %v", agentID, hostname, err)
		b.answer(ctx, cq.ID, fmt.Sprintf("Could not apply updates: %v", err), true)
		return
	}
	if hostname == "" {
		hostname = agentID
	}
	b.answer(ctx, cq.ID, fmt.Sprintf("Applying all updates on %s…", hostname), false)
	// One-shot button: the follow-up result arrives as its own message
	// from the existing companion-result notification, so the old button
	// would only invite a duplicate apply.
	b.clearKeyboard(ctx, cq.Message.Chat.ID, cq.Message.MessageID)
}

func (b *Bot) authorized(cq *callbackQuery) bool {
	if strconv.FormatInt(cq.Message.Chat.ID, 10) != b.chatID {
		return false
	}
	if len(b.allowedUsers) == 0 {
		return true
	}
	if cq.From == nil {
		return false
	}
	_, ok := b.allowedUsers[cq.From.ID]
	return ok
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}

type update struct {
	UpdateID      int64          `json:"update_id"`
	CallbackQuery *callbackQuery `json:"callback_query"`
}

type callbackQuery struct {
	ID      string   `json:"id"`
	From    *user    `json:"from"`
	Message *message `json:"message"`
	Data    string   `json:"data"`
}

type user struct {
	ID int64 `json:"id"`
}

type message struct {
	MessageID int64 `json:"message_id"`
	Chat      chat  `json:"chat"`
}

type chat struct {
	ID int64 `json:"id"`
}

func (b *Bot) getUpdates(ctx context.Context, offset int64) ([]update, error) {
	var out []update
	err := b.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         25,
		"allowed_updates": []string{"callback_query"},
	}, &out)
	return out, err
}

func (b *Bot) answer(ctx context.Context, callbackID, text string, alert bool) {
	err := b.call(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": callbackID,
		"text":              text,
		"show_alert":        alert,
	}, nil)
	if err != nil {
		log.Printf("telegrambot: answerCallbackQuery: %v", err)
	}
}

// clearKeyboard edits the message the button belongs to, dropping its
// inline keyboard. Best effort: a Telegram failure here must not undo an
// apply that was already accepted.
func (b *Bot) clearKeyboard(ctx context.Context, chatID, messageID int64) {
	err := b.call(ctx, "editMessageReplyMarkup", map[string]any{
		"chat_id":      chatID,
		"message_id":   messageID,
		"reply_markup": map[string]any{"inline_keyboard": [][]any{}},
	}, nil)
	if err != nil {
		log.Printf("telegrambot: editMessageReplyMarkup: %v", err)
	}
}

func (b *Bot) call(ctx context.Context, method string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding %s payload: %w", method, err)
	}
	url := fmt.Sprintf("%s/bot%s/%s", b.apiBase, b.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: request failed: %w", method, err)
	}
	defer resp.Body.Close()

	var parsed apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("%s: decoding response (http %s): %w", method, resp.Status, err)
	}
	if !parsed.OK {
		return fmt.Errorf("%s: %s", method, parsed.Description)
	}
	if out != nil && len(parsed.Result) > 0 {
		if err := json.Unmarshal(parsed.Result, out); err != nil {
			return fmt.Errorf("%s: decoding result: %w", method, err)
		}
	}
	return nil
}
