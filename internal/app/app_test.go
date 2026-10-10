package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weixinclawbot/internal/ilink"
	"weixinclawbot/internal/store"
)

func TestSendUsesCachedContextToken(t *testing.T) {
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Msg struct {
				ContextToken string `json:"context_token"`
				ToUserID     string `json:"to_user_id"`
			} `json:"msg"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		seen = body.Msg.ToUserID + "|" + body.Msg.ContextToken
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer upstream.Close()

	st := openStore(t)
	if err := st.SaveCredentials(store.Credentials{BotToken: "tok", BaseURL: upstream.URL, BotID: "bot"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSession("u@im.wechat", "ctx-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	application := &App{Store: st, Client: ilink.NewClient()}
	result, err := application.Send(context.Background(), "", "你好")
	if err != nil {
		t.Fatal(err)
	}
	if result.To != "u@im.wechat" || result.Parts != 1 || result.SentSince != 1 || seen != "u@im.wechat|ctx-1" {
		t.Fatalf("result=%+v seen=%s", result, seen)
	}
}

func TestOldSessionStillTriesUntilWechatRejects(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"ret":-2}`))
			return
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer upstream.Close()

	st := openStore(t)
	_ = st.SaveCredentials(store.Credentials{BotToken: "tok", BaseURL: upstream.URL, BotID: "bot"})
	_ = st.UpsertSession("u@im.wechat", "ctx-1", time.Now().Add(-25*time.Hour))
	application := &App{Store: st, Client: ilink.NewClient()}

	_, err := application.Send(context.Background(), "", "你好")
	if err != ErrTokenRejected || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	_, err = application.Send(context.Background(), "", "再次")
	if err != ErrTokenRejected || calls != 1 {
		t.Fatalf("锁定后不应再请求微信: err=%v calls=%d", err, calls)
	}

	if err := st.UpsertSession("u@im.wechat", "ctx-2", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := application.Send(context.Background(), "", "恢复"); err != nil || calls != 2 {
		t.Fatalf("新消息后应重新发送: err=%v calls=%d", err, calls)
	}
}

func TestRateLimitIsRemembered(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":-2,"errmsg":"rate limited"}`))
	}))
	defer upstream.Close()
	st := openStore(t)
	_ = st.SaveCredentials(store.Credentials{BotToken: "tok", BaseURL: upstream.URL, BotID: "bot"})
	_ = st.UpsertSession("u@im.wechat", "ctx-1", time.Now())
	_, err := (&App{Store: st, Client: ilink.NewClient()}).Send(context.Background(), "", "你好")
	if err != ErrRateLimited {
		t.Fatalf("err=%v", err)
	}
	view := DescribeSession(mustSession(t, st), time.Now())
	if view.Status != "rate_limited" {
		t.Fatalf("status=%s detail=%s", view.Status, view.Detail)
	}
}

func TestHTTPSend(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer upstream.Close()
	st := openStore(t)
	_ = st.SaveCredentials(store.Credentials{BotToken: "tok", BaseURL: upstream.URL, BotID: "bot"})
	_ = st.UpsertSession("u@im.wechat", "ctx-1", time.Now())
	srv := &Server{App: &App{Store: st, Client: ilink.NewClient()}, Token: "local-token"}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"text":"你好"}`))
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"text":"你好"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer local-token")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "ctx-1") || !strings.Contains(body, `"status":"ready"`) || !strings.Contains(body, `"sent_since_inbound":1`) {
		t.Fatalf("会话状态不正确: %s", body)
	}
}

func mustSession(t *testing.T, st *store.Store) store.Session {
	t.Helper()
	item, ok, err := st.LatestSession()
	if err != nil || !ok {
		t.Fatalf("session ok=%v err=%v", ok, err)
	}
	return item
}

func TestSendChoosesAccount(t *testing.T) {
	var got string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer upstream.Close()
	catalog, err := store.OpenCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = catalog.Upsert("工作", store.Credentials{BotToken: "work-token", BaseURL: upstream.URL, BotID: "work@im.bot"})
	_, _ = catalog.Upsert("生活", store.Credentials{BotToken: "life-token", BaseURL: upstream.URL, BotID: "life@im.bot"})
	work, _ := catalog.Open("工作")
	_ = work.Store.UpsertSession("u@im.wechat", "ctx-work", time.Now())
	life, _ := catalog.Open("生活")
	_ = life.Store.UpsertSession("u@im.wechat", "ctx-life", time.Now())

	application := &App{Catalog: catalog, Client: ilink.NewClient()}
	if _, err := application.SendAccount(context.Background(), "", "u@im.wechat", "你好"); !errors.Is(err, store.ErrAccountRequired) {
		t.Fatalf("err=%v", err)
	}
	result, err := application.SendAccount(context.Background(), "生活", "u@im.wechat", "你好")
	if err != nil {
		t.Fatal(err)
	}
	if result.Account != "生活" || got != "Bearer life-token" {
		t.Fatalf("result=%+v auth=%s", result, got)
	}
}

func TestTerminalQRFitsWideConsole(t *testing.T) {
	previous := consoleColumns
	consoleColumns = func() int { return 200 }
	t.Cleanup(func() { consoleColumns = previous })

	var buf strings.Builder
	if err := writeTerminalQR(&buf, "https://weixin.qq.com/x/example"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "\x1b[40m") || !strings.Contains(buf.String(), "\x1b[107m") {
		t.Fatalf("终端二维码缺少颜色块：%s", buf.String())
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}
