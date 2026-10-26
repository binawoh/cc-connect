package weixin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Exercise the configured platform's public send paths: the old factory blocked
// the fifth reply, including permission prompts, even with burst_limit = 0.
func TestNew_ConversationRepliesAndPermissionPromptNotCappedByDefault(t *testing.T) {
	for _, explicitZero := range []bool{false, true} {
		name := "default"
		if explicitZero {
			name = "explicit_zero"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var received []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req sendMessageReq
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode message: %v", err)
					http.Error(w, "invalid message", http.StatusBadRequest)
					return
				}
				mu.Lock()
				received = append(received, req.Msg.ItemList[0].TextItem.Text)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ret":0}`))
			}))
			defer srv.Close()

			opts := map[string]any{"token": "test-token", "base_url": srv.URL, "allow_from": "peer-1"}
			if explicitZero {
				opts["burst_limit"] = 0
			}
			p, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			rc := &replyContext{peerUserID: "peer-1", contextToken: "context-1"}
			messages := []string{"reply 1", "reply 2", "reply 3", "reply 4", "Allow read-only process inspection?", "reply 6", "reply 7", "reply 8"}
			for i, message := range messages {
				if i == 4 {
					err = p.Send(context.Background(), rc, message)
				} else {
					err = p.Reply(context.Background(), rc, message)
				}
				if err != nil {
					t.Fatalf("message %d was blocked: %v", i+1, err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if strings.Join(received, "\n") != strings.Join(messages, "\n") {
				t.Fatalf("delivered messages = %q, want %q", received, messages)
			}
		})
	}
}

func TestNew_ExplicitLocalSendQuotaStillApplies(t *testing.T) {
	p, err := New(map[string]any{"token": "test-token", "allow_from": "peer-1", "burst_limit": 1, "burst_window_secs": 60})
	if err != nil {
		t.Fatal(err)
	}
	platform := p.(*Platform)
	if err := platform.checkSendQuota(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := platform.checkSendQuota(context.Background()); err == nil {
		t.Fatal("explicit local quota should reject the second send")
	}
}

func TestNew_PrepareFailureDoesNotRetryOrSendNotice(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret":-2,"errmsg":"prepare failed"}`))
	}))
	defer srv.Close()
	p, err := New(map[string]any{"token": "test-token", "base_url": srv.URL, "allow_from": "peer-1"})
	if err != nil {
		t.Fatal(err)
	}
	err = p.Reply(context.Background(), &replyContext{peerUserID: "peer-1", contextToken: "context-1"}, "hello")
	if err == nil || !strings.Contains(err.Error(), "ret=-2") {
		t.Fatalf("expected the actual API rejection, got %v", err)
	}
	if strings.Contains(err.Error(), "rate-limited") || strings.Contains(err.Error(), "throttled by") {
		t.Fatalf("prepare failure must not be reported as a confirmed throttle: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("send attempts = %d, want 1 without retry or notice", got)
	}
}
