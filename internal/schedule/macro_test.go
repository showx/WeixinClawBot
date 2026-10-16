package schedule

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMacroSharedValue(t *testing.T) {
	book := openTestBook(t)
	if err := SaveMacros(book.Dir(), []Macro{{Name: "token", Value: "secret-a"}}); err != nil {
		t.Fatal(err)
	}
	var seen string
	book.runCommand = func(_ context.Context, command string) (string, error) {
		seen = command
		return "ok", nil
	}
	saved, err := book.Save(Task{
		Name: "一", Account: "工作", Kind: KindCommand, Body: "echo {token}",
		Prefix: "密钥 {token}", ShowName: true,
		Mode: ModeInterval, EveryMinutes: 60, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := book.RunOne(context.Background(), saved.ID, func(context.Context, string, string, string) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != "echo secret-a" {
		t.Fatalf("命令应换成宏的值: %s", seen)
	}
	logs, err := book.Logs()
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || !strings.Contains(logs[0].Text, "密钥 secret-a") || strings.Contains(logs[0].Text, "{token}") {
		t.Fatalf("发出的内容应已换成宏: %s", logs[0].Text)
	}
	stored, _, err := book.Get(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Body != "echo {token}" || stored.Prefix != "密钥 {token}" {
		t.Fatalf("任务里应保留宏名称: %+v", stored)
	}

	if err := SaveMacros(book.Dir(), []Macro{{Name: "token", Value: "secret-b"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := book.RunOne(context.Background(), saved.ID, func(context.Context, string, string, string) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != "echo secret-b" {
		t.Fatalf("改宏之后应使用新值: %s", seen)
	}
}

func TestMacroExpandsAPIHeader(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	body := ExpandMacros(srv.URL+"\nHeader: Authorization: Bearer {token}\n", []Macro{{Name: "token", Value: "abc"}})
	if _, err := FetchAPI(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer abc" {
		t.Fatalf("请求头: %s", auth)
	}
	encoded := ExpandMacros("https://example.com/a?k=%7Btoken%7D", []Macro{{Name: "token", Value: "abc"}})
	if encoded != "https://example.com/a?k=abc" {
		t.Fatalf("编码后的宏: %s", encoded)
	}
	left := ExpandMacros("day={today} key={missing}", []Macro{{Name: "token", Value: "abc"}})
	if left != "day={today} key={missing}" {
		t.Fatalf("未定义的名称和日期变量应保留: %s", left)
	}
}

func TestMacroTextRoundTrip(t *testing.T) {
	items, err := ParseMacroText("token=abc\nshop=100\n")
	if err != nil {
		t.Fatal(err)
	}
	if FormatMacroText(items) != "token=abc\nshop=100\n" {
		t.Fatalf("排版: %q", FormatMacroText(items))
	}
	if _, err := ParseMacroText("today=1"); err == nil {
		t.Fatal("日期变量不能当作宏")
	}
	if _, err := ParseMacroText("token=abc\ntoken=def"); err == nil {
		t.Fatal("重复名称应拒绝")
	}
	if _, err := ParseMacroText("没有等号"); err == nil {
		t.Fatal("缺少等号应拒绝")
	}
}
