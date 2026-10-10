package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSessionsAndCursor(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCredentials(Credentials{BotToken: "tok", BaseURL: "https://example.test", BotID: "b"}); err != nil {
		t.Fatal(err)
	}
	creds, err := st.LoadCredentials()
	if err != nil || creds.BotToken != "tok" {
		t.Fatalf("creds=%+v err=%v", creds, err)
	}
	older := time.Now().Add(-time.Hour)
	if err := st.UpsertSession("a@im.wechat", "ctx-a", older); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSession("b@im.wechat", "ctx-b", time.Now()); err != nil {
		t.Fatal(err)
	}
	latest, ok, err := st.LatestSession()
	if err != nil || !ok || latest.UserID != "b@im.wechat" || latest.ContextToken != "ctx-b" {
		t.Fatalf("latest=%+v ok=%v err=%v", latest, ok, err)
	}
	if err := st.SaveCursor("buf-1"); err != nil {
		t.Fatal(err)
	}
	cursor, err := st.LoadCursor()
	if err != nil || cursor != "buf-1" {
		t.Fatalf("cursor=%q err=%v", cursor, err)
	}
	token, err := st.APIToken()
	if err != nil || len(token) < 16 {
		t.Fatal(token, err)
	}
	again, err := st.APIToken()
	if err != nil || again != token {
		t.Fatalf("token changed: %s %s", token, again)
	}
	if err := st.NoteSend("b@im.wechat", 2); err != nil {
		t.Fatal(err)
	}
	if err := st.NoteBlocked("b@im.wechat", BlockRateLimited); err != nil {
		t.Fatal(err)
	}
	blocked, ok, err := st.Session("b@im.wechat")
	if err != nil || !ok || blocked.SentCount != 2 || blocked.BlockReason != BlockRateLimited {
		t.Fatalf("blocked=%+v ok=%v err=%v", blocked, ok, err)
	}
	if err := st.UpsertSession("b@im.wechat", "ctx-new", time.Now()); err != nil {
		t.Fatal(err)
	}
	reset, _, err := st.Session("b@im.wechat")
	if err != nil || reset.SentCount != 0 || reset.BlockReason != "" || reset.ContextToken != "ctx-new" {
		t.Fatalf("reset=%+v err=%v", reset, err)
	}
	if _, err := Open(filepath.Join(dir, "nested")); err != nil {
		t.Fatal(err)
	}
}
