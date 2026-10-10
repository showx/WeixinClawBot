package ilink

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientVersion(t *testing.T) {
	if got := ClientVersion("2.4.9"); got != 132105 {
		t.Fatalf("ClientVersion(2.4.9) = %d, want 132105", got)
	}
	if got := ClientVersion("1.0.11"); got != 65547 {
		t.Fatalf("ClientVersion(1.0.11) = %d, want 65547", got)
	}
}

func TestRandomWechatUIN(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(randomWechatUIN())
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || strings.Trim(string(raw), "0123456789") != "" {
		t.Fatalf("uin = %q", raw)
	}
}

func TestSplitText(t *testing.T) {
	parts, err := SplitText("  你好  ", 2000)
	if err != nil || len(parts) != 1 || parts[0] != "你好" {
		t.Fatalf("parts=%v err=%v", parts, err)
	}
	if _, err := SplitText("   ", 2000); err == nil {
		t.Fatal("空白文本应被拒绝")
	}
	long := strings.Repeat("甲", 1500) + "\n" + strings.Repeat("乙", 1500)
	parts, err = SplitText(long, 2000)
	if err != nil || len(parts) != 2 {
		t.Fatalf("parts=%d err=%v", len(parts), err)
	}
	if !strings.HasPrefix(parts[0], "甲") || !strings.HasPrefix(parts[1], "乙") {
		t.Fatalf("分片内容不对: %#v", parts)
	}
}

func TestPeerUserID(t *testing.T) {
	msg := Inbound{FromUserID: "u@im.wechat", ToUserID: "b@im.bot", ContextToken: "t"}
	if got := PeerUserID(msg, "b@im.bot"); got != "u@im.wechat" {
		t.Fatalf("got %s", got)
	}
}

func TestSendText(t *testing.T) {
	var gotBody map[string]any
	var gotAuth, gotType, gotUIN, gotApp, gotVer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			t.Errorf("path %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("AuthorizationType")
		gotUIN = r.Header.Get("X-WECHAT-UIN")
		gotApp = r.Header.Get("iLink-App-Id")
		gotVer = r.Header.Get("iLink-App-ClientVersion")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer srv.Close()

	c := NewClient()
	sent, err := c.SendText(context.Background(), Credentials{
		BotToken: "secret-token",
		BaseURL:  srv.URL,
	}, "user@im.wechat", "ctx-token", "你好")
	if sent != 1 {
		t.Fatalf("sent=%d", sent)
	}
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer secret-token" || gotType != "ilink_bot_token" || gotApp != "bot" || gotVer != "132105" {
		t.Fatalf("headers auth=%s type=%s app=%s ver=%s", gotAuth, gotType, gotApp, gotVer)
	}
	if _, err := base64.StdEncoding.DecodeString(gotUIN); err != nil {
		t.Fatal(err)
	}
	msg := gotBody["msg"].(map[string]any)
	if msg["from_user_id"] != "" || msg["to_user_id"] != "user@im.wechat" || msg["context_token"] != "ctx-token" {
		t.Fatalf("msg = %#v", msg)
	}
	if msg["message_type"] != float64(2) || msg["message_state"] != float64(2) {
		t.Fatalf("type/state = %#v %#v", msg["message_type"], msg["message_state"])
	}
	items := msg["item_list"].([]any)
	item := items[0].(map[string]any)
	text := item["text_item"].(map[string]any)["text"]
	if text != "你好" {
		t.Fatalf("text = %#v", text)
	}
}

func TestGetUpdatesTimeoutIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer srv.Close()
	c := NewClient()
	updates, err := c.GetUpdates(context.Background(), Credentials{BotToken: "t", BaseURL: srv.URL}, "cursor", 30*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if updates.Cursor != "cursor" || len(updates.Messages) != 0 {
		t.Fatalf("%#v", updates)
	}
}

func TestRejectKind(t *testing.T) {
	expired := &APIError{Code: -2}
	if kind, ok := RejectKind(expired); !ok || kind != RejectExpired {
		t.Fatalf("kind=%s ok=%v", kind, ok)
	}
	limited := &APIError{Code: -2, Message: "rate limited"}
	if kind, ok := RejectKind(limited); !ok || kind != RejectRateLimited {
		t.Fatalf("kind=%s ok=%v", kind, ok)
	}
	param := &APIError{Code: -2, Message: "参数错误"}
	if _, ok := RejectKind(param); ok {
		t.Fatal("参数错误不应记成令牌失效")
	}
}

func TestSessionExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":-14,"errmsg":"session timeout"}`))
	}))
	defer srv.Close()
	c := NewClient()
	_, err := c.GetUpdates(context.Background(), Credentials{BotToken: "t", BaseURL: srv.URL}, "", time.Second)
	if !IsSessionExpired(err) {
		t.Fatalf("err = %v", err)
	}
}
