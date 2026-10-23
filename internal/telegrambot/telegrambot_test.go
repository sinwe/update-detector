package telegrambot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAPI records which Bot API methods were called and how. A getUpdates
// call consumes one queued result; once the queue is empty it blocks until
// the client cancels, mirroring Telegram's long poll without busy-spinning
// the test.
type fakeAPI struct {
	srv     *httptest.Server
	answers chan map[string]any
	edits   chan map[string]any
	updates chan string
	polls   atomic.Int32
}

func newFakeAPI(t *testing.T, results ...string) *fakeAPI {
	t.Helper()
	f := &fakeAPI{
		answers: make(chan map[string]any, 16),
		edits:   make(chan map[string]any, 16),
		updates: make(chan string, len(results)),
	}
	for _, r := range results {
		f.updates <- r
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch path.Base(r.URL.Path) {
		case "getUpdates":
			f.polls.Add(1)
			select {
			case next := <-f.updates:
				fmt.Fprintf(w, `{"ok":true,"result":%s}`, next)
			case <-r.Context().Done():
				return
			}
		case "answerCallbackQuery":
			f.answers <- body
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		case "editMessageReplyMarkup":
			f.edits <- body
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		default:
			http.Error(w, "unexpected method", http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) bot(chatID string, allowed []int64) *Bot {
	b := New("token", chatID, allowed)
	b.apiBase = f.srv.URL
	b.client = f.srv.Client()
	return b
}

func callbackQueryJSON(chatID, userID int64, data string) string {
	return fmt.Sprintf(`[{"update_id":11,"callback_query":{"id":"cb1","from":{"id":%d},"message":{"message_id":5,"chat":{"id":%d}},"data":%q}}]`, userID, chatID, data)
}

func TestRunAppliesAuthorizedCallback(t *testing.T) {
	f := newFakeAPI(t, callbackQueryJSON(42, 7, "apply_all:host1"))

	applied := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.bot("42", nil).Run(ctx, func(_ context.Context, agentID string) (string, error) {
			applied <- agentID
			return "Mac-mini", nil
		})
	}()

	select {
	case id := <-applied:
		if id != "host1" {
			t.Errorf("applied agent = %q", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("apply was never called")
	}

	assertCall(t, f.answers, func(body map[string]any) error {
		if body["show_alert"] != false {
			return fmt.Errorf("show_alert = %v", body["show_alert"])
		}
		if text, _ := body["text"].(string); !strings.Contains(text, "Mac-mini") {
			return fmt.Errorf("answer text = %q", text)
		}
		return nil
	})
	assertCall(t, f.edits, func(body map[string]any) error {
		if body["message_id"] != float64(5) {
			return fmt.Errorf("message_id = %v", body["message_id"])
		}
		return nil
	})

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestRunRejectsOtherChats(t *testing.T) {
	f := newFakeAPI(t, callbackQueryJSON(99, 7, "apply_all:host1"))

	applied := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go f.bot("42", nil).Run(ctx, func(context.Context, string) (string, error) {
		applied <- struct{}{}
		return "", nil
	})
	defer cancel()

	assertCall(t, f.answers, func(body map[string]any) error {
		if body["show_alert"] != true {
			return fmt.Errorf("show_alert = %v", body["show_alert"])
		}
		if body["text"] != "Not authorized." {
			return fmt.Errorf("answer text = %v", body["text"])
		}
		return nil
	})
	select {
	case <-applied:
		t.Fatal("apply ran for an unauthorized chat")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRunEnforcesUserAllowlist(t *testing.T) {
	f := newFakeAPI(t, callbackQueryJSON(42, 8, "apply_all:host1"))

	applied := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go f.bot("42", []int64{7}).Run(ctx, func(context.Context, string) (string, error) {
		applied <- struct{}{}
		return "", nil
	})
	defer cancel()

	assertCall(t, f.answers, func(body map[string]any) error {
		if body["show_alert"] != true || body["text"] != "Not authorized." {
			return fmt.Errorf("answer = %v", body)
		}
		return nil
	})
	select {
	case <-applied:
		t.Fatal("apply ran for a user outside the allowlist")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRunReportsApplyErrorAndKeepsButton(t *testing.T) {
	f := newFakeAPI(t, callbackQueryJSON(42, 7, "apply_all:host1"))

	ctx, cancel := context.WithCancel(context.Background())
	go f.bot("42", nil).Run(ctx, func(context.Context, string) (string, error) {
		return "Mac-mini", fmt.Errorf("no companion connected")
	})
	defer cancel()

	assertCall(t, f.answers, func(body map[string]any) error {
		if body["show_alert"] != true {
			return fmt.Errorf("show_alert = %v", body["show_alert"])
		}
		if text, _ := body["text"].(string); !strings.Contains(text, "no companion connected") {
			return fmt.Errorf("answer text = %q", text)
		}
		return nil
	})
	select {
	case body := <-f.edits:
		t.Fatalf("keyboard cleared after a failed apply: %v", body)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRunIgnoresUnknownCallbackData(t *testing.T) {
	f := newFakeAPI(t, callbackQueryJSON(42, 7, "something_else"))

	applied := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go f.bot("42", nil).Run(ctx, func(context.Context, string) (string, error) {
		applied <- struct{}{}
		return "", nil
	})
	defer cancel()

	assertCall(t, f.answers, func(body map[string]any) error {
		if body["text"] != "Unknown action." {
			return fmt.Errorf("answer text = %v", body["text"])
		}
		return nil
	})
	select {
	case <-applied:
		t.Fatal("apply ran for unknown callback data")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestParseApplyAll(t *testing.T) {
	if got, ok := parseApplyAll("apply_all:abc"); !ok || got != "abc" {
		t.Errorf("parseApplyAll = %q, %v", got, ok)
	}
	for _, bad := range []string{"", "apply", "apply_all:", "recheck:abc"} {
		if _, ok := parseApplyAll(bad); ok {
			t.Errorf("parseApplyAll(%q) accepted", bad)
		}
	}
}

func assertCall(t *testing.T, ch <-chan map[string]any, check func(map[string]any) error) {
	t.Helper()
	select {
	case body := <-ch:
		if err := check(body); err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expected Bot API call never happened")
	}
}
