package app

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"weixinclawbot/internal/ilink"
	"weixinclawbot/internal/store"
)

func TestDeskPageAndAccountRemoval(t *testing.T) {
	catalog, err := store.OpenCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Upsert("工作", store.Credentials{BotToken: "tok", BaseURL: "https://example.test", BotID: "work@im.bot", UserID: "owner@im.wechat"}); err != nil {
		t.Fatal(err)
	}
	work, err := catalog.Open("工作")
	if err != nil {
		t.Fatal(err)
	}
	if err := work.Store.UpsertSession("u@im.wechat", "ctx-secret", time.Now()); err != nil {
		t.Fatal(err)
	}
	handler := (&Server{
		App:    &App{Catalog: catalog, Client: ilink.NewClient()},
		Token:  "local-token",
		Listen: "127.0.0.1:8787",
	}).Handler()

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusNotFound {
		t.Fatalf("page=%d", page.Code)
	}

	deskReq := httptest.NewRequest(http.MethodGet, "/v1/desk", nil)
	deskReq.Header.Set("Authorization", "Bearer local-token")
	desk := httptest.NewRecorder()
	handler.ServeHTTP(desk, deskReq)
	body := desk.Body.String()
	if desk.Code != http.StatusOK || !strings.Contains(body, `"name":"工作"`) || !strings.Contains(body, "u@im.wechat") || strings.Contains(body, "ctx-secret") || !strings.Contains(body, `"online":true`) {
		t.Fatalf("desk=%d body=%s", desk.Code, body)
	}

	denied := httptest.NewRecorder()
	deniedReq := httptest.NewRequest(http.MethodGet, "/v1/desk", nil)
	deniedReq.Header.Set("Authorization", "Bearer nope")
	handler.ServeHTTP(denied, deniedReq)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d", denied.Code)
	}

	badLogin := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"name":""}`))
	badReq.Header.Set("Authorization", "Bearer local-token")
	handler.ServeHTTP(badLogin, badReq)
	if badLogin.Code != http.StatusBadRequest {
		t.Fatalf("login=%d body=%s", badLogin.Code, badLogin.Body.String())
	}

	removed := httptest.NewRecorder()
	removeReq := httptest.NewRequest(http.MethodDelete, "/v1/accounts?name="+url.QueryEscape("工作"), nil)
	removeReq.Header.Set("Authorization", "Bearer local-token")
	handler.ServeHTTP(removed, removeReq)
	if removed.Code != http.StatusOK {
		t.Fatalf("remove=%d body=%s", removed.Code, removed.Body.String())
	}
	if _, err := catalog.Open("工作"); !errors.Is(err, store.ErrAccountNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestDeskLoginValidation(t *testing.T) {
	application := &App{Client: ilink.NewClient()}
	if err := application.BeginLogin(""); err == nil {
		t.Fatal("空名称应拒绝")
	}
	if err := application.BeginLogin(`工作\号`); err == nil {
		t.Fatal("斜杠应拒绝")
	}
	if got := application.LoginView(); got.Phase != "idle" {
		t.Fatalf("phase=%s", got.Phase)
	}
	if err := application.SubmitLoginCode("1234"); !errors.Is(err, errLoginNoCode) {
		t.Fatalf("err=%v", err)
	}
	application.CancelLogin()
	if got := application.LoginView(); got.Phase != "idle" {
		t.Fatalf("取消后 phase=%s", got.Phase)
	}
}

func TestCancelLoginHidesQRImmediately(t *testing.T) {
	application := &App{Client: ilink.NewClient()}
	desk := application.desktop()
	_, cancel := context.WithCancel(context.Background())
	desk.mu.Lock()
	desk.running = true
	desk.phase = "qr"
	desk.message = "请用微信扫描二维码。"
	desk.qr = []byte("png")
	desk.cancel = cancel
	desk.mu.Unlock()

	application.CancelLogin()
	view := application.LoginView()
	if view.Phase != "idle" || view.Message != "" || view.QR != "" {
		t.Fatalf("关闭后仍显示扫码: %+v", view)
	}
}

func TestSuperviseStopsWhenCancelled(t *testing.T) {
	catalog, err := store.OpenCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	application := &App{Catalog: catalog, Client: ilink.NewClient(), Log: log.New(io.Discard, "", 0)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		done <- application.Supervise(ctx)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("取消后没有退出")
	}
}
