package schedule

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		"extra:",
		"n: 1",
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
	if err := task.Validate(); err != nil {
		t.Fatal(err)
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

func TestScaleFenToYuan(t *testing.T) {
	field, err := parseAPIFieldLine("消耗 = data.list.*.cost/100")
	if err != nil || field.Path != "data.list.*.cost" || field.Op != '/' || field.Factor != 100 || field.Spec() != "data.list.*.cost/100" {
		t.Fatalf("换算没认出来: %+v %v", field, err)
	}
	payload := map[string]any{
		"data": map[string]any{
			"list": []any{
				map[string]any{
					"game_name": "测试",
					"cost":      json.Number("357267"),
					"revenue":   json.Number("15982"),
				},
			},
		},
	}
	got := renderFields([]apiField{
		{Label: "游戏", Path: "data.list.*.game_name"},
		field,
		{Label: "收入", Path: "data.list.*.revenue", Op: '/', Factor: 100},
	}, payload)
	want := "游戏: 测试  消耗: 3572.67  收入: 159.82"
	if got != want {
		t.Fatalf("分没有换成元:\n%s", got)
	}
	dollar, err := parseAPIFieldLine("消耗 = data.list.*.cost/100美元")
	if err != nil || dollar.Unit != "美元" || dollar.Spec() != "data.list.*.cost/100美元" {
		t.Fatalf("单位没认出来: %+v %v", dollar, err)
	}
	got = renderFields([]apiField{dollar}, payload)
	if got != "消耗: 3572.67美元" {
		t.Fatalf("单位没跟上:\n%s", got)
	}
	yuan, err := parseAPIFieldLine("金额 = data.list.*.amount元")
	if err != nil || yuan.Path != "data.list.*.amount" || yuan.Op != 0 || yuan.Unit != "元" || yuan.Spec() != "data.list.*.amount元" {
		t.Fatalf("字段名后的单位没认出来: %+v %v", yuan, err)
	}
	got = renderFields([]apiField{yuan}, map[string]any{
		"data": map[string]any{
			"list": []any{map[string]any{"amount": "27465.55"}},
		},
	})
	if got != "金额: 27465.55元" {
		t.Fatalf("元没有跟上:\n%s", got)
	}
}

func TestRenderPairedListFields(t *testing.T) {
	payload := map[string]any{
		"data": map[string]any{
			"sum": "105780.78",
			"list": []any{
				map[string]any{"amount": "27465.55", "deptname": "肆狼三组"},
				map[string]any{"amount": "8934.22", "deptname": "寒锋组"},
			},
		},
	}
	got := renderFields([]apiField{
		{Label: "部门", Path: "data.list.*.deptname"},
		{Label: "金额", Path: "data.list.*.amount"},
		{Label: "合计", Path: "data.sum"},
	}, payload)
	want := "部门: 肆狼三组  金额: 27465.55\n部门: 寒锋组  金额: 8934.22\n合计: 105780.78"
	if got != want {
		t.Fatalf("明细没有排在一行:\n%s", got)
	}
}

func TestFetchAPILabelsWildcardAndPost(t *testing.T) {
	var gotMethod, gotBody, gotType, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotType = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"total":2,"list":[{"name":"投放"},{"name":"品牌"},{"name":"活动"}]}}`))
	}))
	defer srv.Close()

	form := APIForm{
		Method:  "POST",
		URL:     srv.URL,
		Headers: "Authorization: Bearer token",
		Body:    "{\"day\":\"today\"}",
		Fields:  "收入 = data.total\n名单 = data.list.*.name",
	}
	body, err := EncodeAPI(form, true)
	if err != nil {
		t.Fatal(err)
	}
	text, err := FetchAPI(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotBody != `{"day":"today"}` || !strings.Contains(gotType, "application/json") || gotAuth != "Bearer token" {
		t.Fatalf("请求不对: %s %s %s %s", gotMethod, gotType, gotAuth, gotBody)
	}
	if !strings.Contains(text, "收入: 2") || !strings.Contains(text, "- 投放") || !strings.Contains(text, "- 品牌") {
		t.Fatalf("结果不好读:\n%s", text)
	}
}

func TestPreviewAndRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"total":8,"list":[{"name":"投放"}]}}`))
	}))
	defer srv.Close()

	old := srv.URL + "\ndata.total\nHeader: Authorization: Bearer token"
	form := DecodeAPI(old)
	if form.URL != srv.URL || form.Fields != "data.total" || form.Headers != "Authorization: Bearer token" || form.Method != http.MethodGet {
		t.Fatalf("旧写法应拆开: %+v", form)
	}
	stored, err := EncodeAPI(form, true)
	if err != nil {
		t.Fatal(err)
	}
	bare, err := EncodeAPI(APIForm{URL: form.URL, Headers: form.Headers}, false)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewAPI(context.Background(), bare)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.Fields, "data.total") || !strings.Contains(preview.Fields, "data.list.*.name") {
		t.Fatalf("应列出能用的字段:\n%s", preview.Detail)
	}
	if !strings.Contains(preview.Detail, "没有勾选字段") || !strings.Contains(preview.Message, `"total"`) {
		t.Fatalf("没勾字段时应发送整段 JSON:\n%s\n%s", preview.Message, preview.Detail)
	}
	sent, err := FetchAPI(context.Background(), bare)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(sent) != strings.TrimSpace(preview.Raw) || !strings.Contains(sent, "list") {
		t.Fatalf("没勾字段时应把 JSON 原文发出去:\n%s", sent)
	}
	full, err := PreviewAPI(context.Background(), stored)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full.Message, "data.total: 8") || !strings.Contains(full.Detail, "会发给微信") {
		t.Fatalf("试取结果不对:\n%s\n%s", full.Message, full.Detail)
	}
	if preview.Status != http.StatusOK || !strings.Contains(preview.Raw, "total") {
		t.Fatalf("应带回状态和原文: %d\n%s", preview.Status, preview.Raw)
	}
	merged := MergeAPIFields("收入 = data.total", preview.Fields)
	if !strings.Contains(merged, "收入 = data.total") || strings.Count(merged, "data.total") != 1 {
		t.Fatalf("已有路径不应重复: %s", merged)
	}
	if !strings.Contains(merged, "data.list.*.name") {
		t.Fatalf("新字段应补上: %s", merged)
	}
}

func TestAPIQueryAndUncheckedRows(t *testing.T) {
	base, rows, err := SplitAPIQuery("https://example.com/api?b=2&a=hello+world")
	if err != nil {
		t.Fatal(err)
	}
	if base != "https://example.com/api" || len(rows) != 2 || rows[0].Key != "b" || rows[1].Value != "hello world" {
		t.Fatalf("查询串拆开不对: %s %#v", base, rows)
	}
	rows = append(rows, APIRow{Enabled: false, Key: "c", Value: "3"})
	joined, err := JoinAPIQuery(base, rows)
	if err != nil {
		t.Fatal(err)
	}
	if joined != "https://example.com/api?b=2&a=hello+world" {
		t.Fatalf("没勾选的参数不该进网址: %s", joined)
	}

	form := APIForm{
		Method:  "GET",
		URL:     "https://example.com/api?a=1",
		Params:  "b=2",
		Headers: "Authorization: Bearer t\n// X-Debug: 1",
		Fields:  "data.total\n// 备注 = data.note",
	}
	stored, err := EncodeAPI(form, true)
	if err != nil {
		t.Fatal(err)
	}
	again := DecodeAPI(stored)
	if !strings.Contains(again.URL, "a=1") || strings.Contains(again.URL, "b=2") {
		t.Fatalf("网址应只保留勾选的参数: %s", again.URL)
	}
	if again.Params != "b=2" {
		t.Fatalf("取消勾选的参数应保留: %s", again.Params)
	}
	if !strings.Contains(again.Headers, "Authorization: Bearer t") || !strings.Contains(again.Headers, "// X-Debug: 1") {
		t.Fatalf("请求头勾选状态不对: %s", again.Headers)
	}
	if !strings.Contains(again.Fields, "data.total") || !strings.Contains(again.Fields, "备注 = data.note") {
		t.Fatalf("字段勾选状态不对: %s", again.Fields)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("b") != "" || r.Header.Get("X-Debug") != "" {
			http.Error(w, "off", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"missing"}`))
	}))
	defer srv.Close()
	preview, err := PreviewAPI(context.Background(), srv.URL+"?a=1")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Status != http.StatusNotFound || !strings.Contains(preview.Raw, "missing") {
		t.Fatalf("失败响应也要能看: %d\n%s", preview.Status, preview.Raw)
	}
}

func TestAPIDateVars(t *testing.T) {
	previous := apiClock
	apiClock = func() time.Time { return time.Date(2026, 10, 14, 16, 0, 0, 0, time.Local) }
	t.Cleanup(func() { apiClock = previous })

	var gotPath, gotDay, gotHeader, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotDay = r.URL.Query().Get("day")
		gotHeader = r.Header.Get("X-Day")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	body := "POST " + srv.URL + "/report/{yesterday}?day={today}\n" +
		"Header: X-Day: {yesterday}\n" +
		`Body: {"d":"{today}","y":"{yesterday}"}`
	text, err := FetchAPI(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `"ok": true`) && !strings.Contains(text, `"ok":true`) {
		t.Fatalf("返回: %s", text)
	}
	if gotPath != "/report/20261013" || gotDay != "20261014" || gotHeader != "20261013" {
		t.Fatalf("地址或请求头没换成日期: path=%s day=%s header=%s", gotPath, gotDay, gotHeader)
	}
	if strings.Contains(gotBody, "{today}") || strings.Contains(gotBody, "{yesterday}") || !strings.Contains(gotBody, "20261014") || !strings.Contains(gotBody, "20261013") {
		t.Fatalf("正文没换成日期: %s", gotBody)
	}

	preview, err := PreviewAPI(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.Vars, "{today}=20261014") || !strings.Contains(preview.Vars, "{yesterday}=20261013") {
		t.Fatalf("预览应说明替换结果: %s", preview.Vars)
	}

	dashSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("plain") != "20261013" || r.URL.Query().Get("dash") != "2026-10-13" || r.URL.Query().Get("today") != "2026-10-14" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer dashSrv.Close()
	dashBody := dashSrv.URL + "?plain={yesterday}&dash={yesterday-yyyy-mm-dd}&today={today-yyyy-mm-dd}"
	if _, err := FetchAPI(context.Background(), dashBody); err != nil {
		t.Fatal(err)
	}
	dashPreview, err := PreviewAPI(context.Background(), dashBody)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dashPreview.Vars, "{yesterday}=20261013") || !strings.Contains(dashPreview.Vars, "{yesterday-yyyy-mm-dd}=2026-10-13") || !strings.Contains(dashPreview.Vars, "{today-yyyy-mm-dd}=2026-10-14") {
		t.Fatalf("两种日期格式应分开说明: %s", dashPreview.Vars)
	}
	if strings.Contains(dashPreview.Vars, "{today}=20261014") {
		t.Fatalf("带横线的变量不应再记成不带横线: %s", dashPreview.Vars)
	}

	stored, err := EncodeAPI(APIForm{Method: http.MethodGet, URL: "https://example.com/report/{yesterday}"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "20261013") || (!strings.Contains(stored, "{yesterday}") && !strings.Contains(stored, "%7Byesterday%7D")) {
		t.Fatalf("保存时要留下变量，不要写成具体日期: %s", stored)
	}
}

func TestAPICurlShowsExpandedRequest(t *testing.T) {
	previous := apiClock
	apiClock = func() time.Time { return time.Date(2026, 10, 14, 16, 0, 0, 0, time.Local) }
	t.Cleanup(func() { apiClock = previous })

	curl, err := APICurl("POST https://example.com/report/{yesterday}\n" +
		"Header: Authorization: Bearer a'b\n" +
		`Body: {"day":"{today}"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"curl.exe --request POST",
		"--url 'https://example.com/report/20261013'",
		"--header 'Authorization: Bearer a''b'",
		"--header 'Accept: application/json, text/json;q=0.9, */*;q=0.1'",
		"--header 'Content-Type: application/json; charset=utf-8'",
		`--data-raw '{"day":"20261014"}'`,
	} {
		if !strings.Contains(curl, want) {
			t.Fatalf("缺少 %s\n%s", want, curl)
		}
	}
	if strings.Contains(curl, "{today}") || strings.Contains(curl, "{yesterday}") {
		t.Fatalf("curl 里还应留着变量: %s", curl)
	}
}

func TestAPIRunLogKeepsCurlOutOfWechat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer srv.Close()
	book := openTestBook(t)
	saved, err := book.Save(Task{
		Name: "接口", Account: "工作", Enabled: true,
		Kind: KindAPI, Body: srv.URL + "\ndata.ok",
		Mode: ModeDaily, At: "08:30",
	})
	if err != nil {
		t.Fatal(err)
	}
	var sent string
	if _, err := book.RunOne(context.Background(), saved.ID, func(_ context.Context, _, _, text string) error {
		sent = text
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sent, "curl") {
		t.Fatalf("不应把 curl 发给微信: %s", sent)
	}
	logs, err := book.Logs()
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || !strings.Contains(logs[0].Text, "curl.exe --request GET") || !strings.Contains(logs[0].Text, srv.URL) {
		t.Fatalf("日志应留下完整 curl: %+v", logs)
	}
}
