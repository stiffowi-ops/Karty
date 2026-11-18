package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type startupTransport struct {
	blocked           chan struct{}
	menu              chan string
	firstNotification sync.Once
	polls             int // Only the polling loop calls getUpdates.
}

func (tr *startupTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	result := `true`
	switch filepath.Base(r.URL.Path) {
	case "getMe":
		result = `{"id":500,"username":"test_bot"}`
	case "getUpdates":
		select {
		case <-tr.blocked:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		tr.polls++
		if tr.polls > 1 {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		result = `[{"update_id":42,"message":{"text":"/start","from":{"id":77},"chat":{"id":77,"type":"private"}}}]`
	case "sendMessage":
		var p struct {
			ChatID int64           `json:"chat_id"`
			Markup json.RawMessage `json:"reply_markup"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			return nil, err
		}
		if p.ChatID == 200 || p.ChatID == 300 {
			tr.firstNotification.Do(func() { close(tr.blocked) })
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		if strings.Contains(string(p.Markup), "inline_keyboard") {
			tr.menu <- string(p.Markup)
		}
		result = `{"message_id":123}`
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":` + result + `}`))}, nil
}

func TestPollingRespondsToStartDuringPendingNotification(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	serviceLead(t, b, 0)
	b.offsetFile = filepath.Join(t.TempDir(), "offset.txt")
	b.store = nil // No catalogue backups or external sync in this transport test.
	tr := &startupTransport{blocked: make(chan struct{}), menu: make(chan string, 1)}
	b.http = &http.Client{Transport: tr}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("polling did not stop")
		}
	}()
	select {
	case m := <-tr.menu:
		if !strings.Contains(m, "cab:home") || !strings.Contains(m, "success") {
			t.Fatalf("missing green requests button in menu: %s", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("backlog blocked /start")
	}
}
