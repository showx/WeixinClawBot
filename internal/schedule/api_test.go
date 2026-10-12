package schedule

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchAPIFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"total":1280,"ok":true,"title":"早报","list":[{"name":"投放"}],"empty":null},"extra":{"n":1}}`))
	}))
	defer srv.Close()

	body := srv.URL + "\n" +
		"data.total\n" +
		"data.title\n" +
		"data.ok\n" +
		"data.list.0.name\n" +
		"data.empty\n" +
		"data.missing\n" +
		"extra\n" +
		"Header: Authorization: Bearer token"
	text, err := FetchAPI(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"data.total: 1280",
		"data.title: 早报",
		"data.ok: true",
		"data.list.0.name: 投放",
		"data.empty: （空）",
		"data.missing: （没有这个字段）",
		`extra: {"n":1}`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺少 %s\n实际: %s", want, text)
		}
	}
}

func TestFetchAPIRejects(t *testing.T) {
	task := Task{Name: "接口", Account: "工作", Kind: KindAPI, Mode: ModeDaily, At: "08:30"}
	task.Body = "https://example.com/api\ndata.total"
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	task.Body = "https://example.com/api"
	if err := task.Validate(); err == nil {
		t.Fatal("没有字段应该被拒绝")
	}
	task.Body = "https://example.com/api\ndata..total"
	if err := task.Validate(); err == nil {
		t.Fatal("空字段段应该被拒绝")
	}
	task.Body = "https://example.com/api\ndata.total\nHeader: Authorization"
	if err := task.Validate(); err == nil {
		t.Fatal("不完整的请求头应该被拒绝")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("bad"))
	}))
	defer srv.Close()
	if _, err := FetchAPI(context.Background(), srv.URL+"\ndata.total"); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("错误状态应说明状态码: %v", err)
	}

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer plain.Close()
	if _, err := FetchAPI(context.Background(), plain.URL+"\ndata.total"); err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("非 JSON 应被拒绝: %v", err)
	}
}

func TestAPITaskSendsFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"income":100}`))
	}))
	defer srv.Close()

	book := openTestBook(t)
	saved, err := book.Save(Task{
		Name: "收入", Account: "工作", Kind: KindAPI,
		Body: srv.URL + "\nincome",
		Mode: ModeInterval, EveryMinutes: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	result, err := book.RunOne(context.Background(), saved.ID, func(_ context.Context, _, _, body string) error {
		got = body
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "income: 100" || !strings.Contains(result, "已发送") {
		t.Fatalf("result=%s body=%s", result, got)
	}
}
