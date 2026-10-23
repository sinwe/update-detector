package notifier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureSend returns a notifier wired to a fake Bot API that records the
// decoded sendMessage body.
func captureSend(t *testing.T) (*Telegram, *map[string]any) {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding sendMessage body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	t.Cleanup(srv.Close)

	n := NewTelegram("token", "42")
	n.apiBase = srv.URL
	n.client = srv.Client()
	return n, &body
}

func TestSendOmitsReplyMarkupWithoutApplyTarget(t *testing.T) {
	n, body := captureSend(t)
	if err := n.Send(context.Background(), Event{Hostname: "host", Changes: []string{"x"}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, ok := (*body)["reply_markup"]; ok {
		t.Fatalf("reply_markup present on a plain event: %v", *body)
	}
}

func TestSendAddsApplyButton(t *testing.T) {
	n, body := captureSend(t)
	if err := n.Send(context.Background(), Event{
		Hostname:     "host",
		Changes:      []string{"1 new package update available"},
		ApplyAgentID: "abc123",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	markup, ok := (*body)["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("no reply_markup in payload: %v", *body)
	}
	rows, ok := markup["inline_keyboard"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("inline_keyboard = %v", markup["inline_keyboard"])
	}
	row, ok := rows[0].([]any)
	if !ok || len(row) != 1 {
		t.Fatalf("inline_keyboard row = %v", rows[0])
	}
	button, ok := row[0].(map[string]any)
	if !ok {
		t.Fatalf("button = %v", row[0])
	}
	if got := button["text"]; got != "Apply all updates" {
		t.Errorf("button text = %v", got)
	}
	if got := button["callback_data"]; got != "apply_all:abc123" {
		t.Errorf("callback_data = %v", got)
	}
}
