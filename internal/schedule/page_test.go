package schedule

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestFetchPageReadsArticle(t *testing.T) {
	const page = `<!DOCTYPE html><html><head>
<title>早报</title>
<style>body{color:red}</style>
<script>var secret = "不要这段"</script>
</head><body>
<nav>首页 登录</nav>
<article><h1>今日天气</h1><p>晴，25度。鱼&amp;虾</p><img alt="配图说明"></article>
<footer>版权所有</footer>
</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	text, err := FetchPage(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"早报", "今日天气", "晴，25度", "鱼&虾", "配图说明"} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺少 %s\n%s", want, text)
		}
	}
	for _, hidden := range []string{"不要这段", "首页", "版权所有", "color:red"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("不该出现 %s\n%s", hidden, text)
		}
	}

	book := openTestBook(t)
	saved, err := book.Save(Task{
		Name: "页面", Account: "工作", Enabled: true,
		Kind: KindPage, Body: srv.URL, Mode: ModeInterval, EveryMinutes: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	var sent string
	_, err = book.RunOne(context.Background(), saved.ID, func(_ context.Context, account, to, body string) error {
		if account != "工作" || to != "" {
			t.Fatalf("发送目标不对: %s %s", account, to)
		}
		sent = body
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sent != text {
		t.Fatalf("发出的文字与读到的不一致:\n%s\n%s", sent, text)
	}
}

func TestFetchPageGBKAndPlain(t *testing.T) {
	page := "<html><head><title>早报</title></head><body><p>今天晴</p></body></html>"
	raw, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(page))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/plain":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("  第一行\r\n\r\n\r\n第二行  "))
		case "/image":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
		default:
			w.Header().Set("Content-Type", "text/html; charset=gbk")
			_, _ = w.Write(raw)
		}
	}))
	defer srv.Close()

	text, err := FetchPage(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "今天晴") || !strings.Contains(text, "早报") {
		t.Fatalf("GBK 页面没有读出文字: %s", text)
	}
	plain, err := FetchPage(context.Background(), srv.URL+"/plain")
	if err != nil {
		t.Fatal(err)
	}
	if plain != "第一行\n\n第二行" {
		t.Fatalf("纯文本不对: %q", plain)
	}
	if _, err := FetchPage(context.Background(), srv.URL+"/image"); err == nil || !strings.Contains(err.Error(), "图片") {
		t.Fatalf("图片地址应说明没有文字: %v", err)
	}
}

func TestScriptPageUsesBrowserText(t *testing.T) {
	if findBrowser() == "" {
		t.Skip("没有 Edge 或 Chrome")
	}
	const page = `<!doctype html><html><head><title>只有标题</title></head><body><div id="app"></div><script>
document.getElementById("app").textContent = "报表正文 收入 100";
</script></body></html>`
	const cookiePage = `<!doctype html><html><head><title>登录</title></head><body><div id="app"></div><script>
document.getElementById("app").textContent = "看到 " + document.cookie;
</script></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if strings.HasSuffix(r.URL.Path, "/cookie") {
			_, _ = w.Write([]byte(cookiePage))
			return
		}
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	plain := extractHTMLText(page)
	if strings.Contains(plain, "报表正文") {
		t.Fatalf("源码提取不该读到脚本写上的正文: %s", plain)
	}
	text, err := FetchPage(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "报表正文") || !strings.Contains(text, "收入 100") {
		t.Fatalf("脚本页面没有读出正文: %s", text)
	}

	text, err = FetchPage(context.Background(), srv.URL+"/cookie\nCookie: session=abc")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "看到") || !strings.Contains(text, "session=abc") {
		t.Fatalf("Cookie 没有带进页面: %s", text)
	}
}

func TestPageCookieLine(t *testing.T) {
	target, err := parsePageBody("https://example.com/report\nCookie: session=abc")
	if err != nil {
		t.Fatal(err)
	}
	if target.URL != "https://example.com/report" || target.Cookie != "session=abc" {
		t.Fatalf("解析不对: %+v", target)
	}
	task := Task{Name: "报表", Account: "工作", Kind: KindPage, Body: "https://example.com/report\n备注", Mode: ModeDaily, At: "08:30"}
	if err := task.Validate(); err == nil {
		t.Fatal("多余的一行应该被拒绝")
	}
}

func TestPageTaskValidationAndEmpty(t *testing.T) {
	ok := Task{Name: "早报", Account: "工作", Kind: KindPage, Body: "https://example.com/a", Mode: ModeDaily, At: "08:30"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := ok
	bad.Body = "file:///C:/page.html"
	if err := bad.Validate(); err == nil {
		t.Fatal("非网页地址应该被拒绝")
	}
	bad.Body = "https://user:secret@example.com"
	if err := bad.Validate(); err == nil {
		t.Fatal("带密码的地址应该被拒绝")
	}

	book := openTestBook(t)
	saved, err := book.Save(ok)
	if err != nil {
		t.Fatal(err)
	}
	book.fetchPage = func(context.Context, string) (string, error) { return "  ", nil }
	var sent int
	if _, err := book.RunOne(context.Background(), saved.ID, func(context.Context, string, string, string) error {
		sent++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatal("没有文字时不应该发送")
	}
	got, _, _ := book.Get(saved.ID)
	if got.LastResult != "页面没有识别出文字，未发送" {
		t.Fatalf("结果不对: %s", got.LastResult)
	}

	book.fetchPage = func(context.Context, string) (string, error) {
		return "", errors.New("连接失败")
	}
	var text string
	if _, err := book.RunOne(context.Background(), saved.ID, func(_ context.Context, _, _, body string) error {
		text = body
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "页面抓取失败") || !strings.Contains(text, "连接失败") {
		t.Fatalf("应把失败说明发给微信: %s", text)
	}
}
