package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogSeparatesWechats(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(`{"bot_token":"old","base_url":"https://old.test","bot_id":"old@im.bot","user_id":"old@im.wechat"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions.json"), []byte(`{"users":{"u@im.wechat":{"user_id":"u@im.wechat","context_token":"ctx","updated_at":"2026-10-10T00:00:00Z"}},"default_user_id":"u@im.wechat"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := OpenCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("旧登录文件应被迁走: %v", err)
	}
	list, err := catalog.List()
	if err != nil || len(list) != 1 || list[0].Name != "微信1" {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	bound, err := catalog.Open("微信1")
	if err != nil {
		t.Fatal(err)
	}
	session, ok, err := bound.Store.Session("u@im.wechat")
	if err != nil || !ok || session.ContextToken != "ctx" {
		t.Fatalf("迁过来的会话不对: %+v ok=%v err=%v", session, ok, err)
	}

	if _, err := catalog.Upsert("工作", Credentials{BotToken: "work", BaseURL: "https://work.test", BotID: "work@im.bot", UserID: "work@im.wechat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Upsert("生活", Credentials{BotToken: "life", BaseURL: "https://life.test", BotID: "life@im.bot", UserID: "life@im.wechat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Open(""); !errors.Is(err, ErrAccountRequired) {
		t.Fatalf("多个微信时应要求指定名称: %v", err)
	}
	work, err := catalog.Open("工作")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := work.Store.LoadCredentials()
	if err != nil || creds.BotToken != "work" {
		t.Fatalf("creds=%+v err=%v", creds, err)
	}
	if _, err := catalog.Upsert("工作", Credentials{BotToken: "other", BaseURL: "https://other.test", BotID: "other@im.bot"}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("重名应拒绝: %v", err)
	}
	if err := catalog.Remove("生活"); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Open("生活"); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("移除后不应再打开: %v", err)
	}
	if err := catalog.Remove("生活"); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("再次移除应找不到: %v", err)
	}
	if err := ValidateAccountName(" "); err == nil {
		t.Fatal("空名称应拒绝")
	}
	if err := ValidateAccountName("a/b"); err == nil {
		t.Fatal("斜杠名称应拒绝")
	}
	renamed, err := catalog.Upsert("工作号", Credentials{BotToken: "work2", BaseURL: "https://work.test", BotID: "work@im.bot", UserID: "work@im.wechat"})
	if err != nil || renamed.Name != "工作号" {
		t.Fatalf("再次登录应更新原微信: %+v err=%v", renamed, err)
	}
}
