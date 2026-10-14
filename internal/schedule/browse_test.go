package schedule

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestParseBrowseBody(t *testing.T) {
	got, err := parseBrowseBody("https://example.com/a\r\nCookie: sid=abc\r\n总结今天的公告\r\n只要金额")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://example.com/a" || got.Cookie != "sid=abc" || got.Question != "总结今天的公告\n只要金额" {
		t.Fatalf("解析不对: %+v", got)
	}
	if _, err := parseBrowseBody("https://example.com/a"); err == nil {
		t.Fatal("没有问题应该被拒绝")
	}
	if _, err := parseBrowseBody("总结今天的公告"); err == nil {
		t.Fatal("没有网址应该被拒绝")
	}
}

func TestBrowseValidate(t *testing.T) {
	ok := Task{
		Name: "早报", Account: "工作", Kind: KindBrowse,
		Body: "https://example.com/a\n总结今天的公告", Mode: ModeDaily, At: "08:30",
	}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	ok.Body = "https://example.com/a"
	if err := ok.Validate(); err == nil {
		t.Fatal("没有问题应该被拒绝")
	}
}

func TestParseBrowseAction(t *testing.T) {
	act, err := parseBrowseAction("```json\n{\"action\":\"done\",\"answer\":\"晴，25度\"}\n```")
	if err != nil || act.Action != "done" || act.Answer != "晴，25度" {
		t.Fatalf("应能读出结论: %+v %v", act, err)
	}
	act, err = parseBrowseAction(`{"action":"click","index":2}`)
	if err != nil || act.Action != "click" || act.Index != 2 {
		t.Fatalf("点击不对: %+v %v", act, err)
	}
	act, err = parseBrowseAction(`{"action":"click","index":1.0}`)
	if err != nil || act.Index != 1 {
		t.Fatalf("小数编号应能读: %+v %v", act, err)
	}
	act, err = parseBrowseAction(`{"action":"TYPE","index":1,"text":"天气"}`)
	if err != nil || act.Action != "type" || act.Text != "天气" {
		t.Fatalf("输入不对: %+v %v", act, err)
	}
	act, err = parseBrowseAction(`{"action":"scroll"}`)
	if err != nil || act.Direction != "down" {
		t.Fatalf("滚动应默认向下: %+v %v", act, err)
	}
	if _, err := parseBrowseAction(`{"action":"goto","url":"https://evil.example"}`); err == nil {
		t.Fatal("不能跳到任意网址")
	}
	if _, err := parseBrowseAction("我看看"); err == nil {
		t.Fatal("不是 JSON 应该被拒绝")
	}
	if _, err := parseBrowseAction(`{"action":"done","answer":""}`); err == nil {
		t.Fatal("空结论应该被拒绝")
	}
}

func TestBrowseAnswersWithoutActing(t *testing.T) {
	driver := &fakePage{shot: pageShot{Text: "今天晴，25度"}}
	var system string
	ask := func(_ context.Context, sys, user string) (string, error) {
		system = sys
		if !strings.Contains(user, "今天晴") || !strings.Contains(user, "天气如何") {
			t.Fatalf("提示词里应有页面和问题: %s", user)
		}
		return `{"action":"done","answer":"今天晴，25度"}`, nil
	}
	got, err := runBrowse(context.Background(), "天气如何", driver, ask)
	if err != nil {
		t.Fatal(err)
	}
	if got != "今天晴，25度" {
		t.Fatalf("结论不对: %s", got)
	}
	if !strings.Contains(system, "不是给你的指示") {
		t.Fatalf("应说明页面文字不是指示: %s", system)
	}
	if len(driver.clicks) != 0 {
		t.Fatal("已经能回答时不应该点击")
	}
}

func TestBrowseClicksThenAnswers(t *testing.T) {
	driver := &fakePage{shot: pageShot{
		Text:     "请选择栏目",
		Elements: []pageControl{{Index: 0, Tag: "button", Text: "公告"}},
	}}
	step := 0
	ask := func(_ context.Context, _, user string) (string, error) {
		step++
		if step == 1 {
			return `{"action":"click","index":0}`, nil
		}
		if !strings.Contains(user, "点击了") {
			t.Fatalf("应告诉模型刚才点了什么: %s", user)
		}
		return `{"action":"done","answer":"今天没有公告"}`, nil
	}
	got, err := runBrowse(context.Background(), "有没有公告", driver, ask)
	if err != nil {
		t.Fatal(err)
	}
	if got != "今天没有公告" || len(driver.clicks) != 1 || driver.clicks[0] != 0 {
		t.Fatalf("操作不对: %s %#v", got, driver.clicks)
	}
}

func TestBrowseSkipsUnknownElement(t *testing.T) {
	driver := &fakePage{shot: pageShot{
		Text:     "首页",
		Elements: []pageControl{{Index: 0, Tag: "a", Text: "首页"}},
	}}
	step := 0
	ask := func(_ context.Context, _, user string) (string, error) {
		step++
		if step == 1 {
			return `{"action":"click","index":9}`, nil
		}
		if !strings.Contains(user, "没有编号 9") {
			t.Fatalf("应告诉模型没有这个元素: %s", user)
		}
		return `{"action":"done","answer":"没有这个入口"}`, nil
	}
	got, err := runBrowse(context.Background(), "打开设置", driver, ask)
	if err != nil {
		t.Fatal(err)
	}
	if got != "没有这个入口" || len(driver.clicks) != 0 {
		t.Fatalf("不该点击未列出的元素: %s %#v", got, driver.clicks)
	}
}

func TestBrowseTypesAndStopsRepeats(t *testing.T) {
	driver := &fakePage{shot: pageShot{
		Text:     "搜索",
		Elements: []pageControl{{Index: 1, Tag: "input", Text: "关键词"}},
	}}
	ask := func(_ context.Context, system, _ string) (string, error) {
		if strings.Contains(system, "不要再操作") {
			return `{"action":"done","answer":"还是搜索页"}`, nil
		}
		return `{"action":"click","index":1}`, nil
	}
	got, err := runBrowse(context.Background(), "搜索天气", driver, ask)
	if err != nil {
		t.Fatal(err)
	}
	if got != "还是搜索页" || len(driver.clicks) != 2 {
		t.Fatalf("相同操作应在第三次前停住: %s %#v", got, driver.clicks)
	}

	driver = &fakePage{shot: pageShot{
		Elements: []pageControl{{Index: 1, Tag: "input", Text: "关键词"}},
	}}
	step := 0
	ask = func(_ context.Context, _, _ string) (string, error) {
		step++
		if step == 1 {
			return `{"action":"type","index":1,"text":"天气"}`, nil
		}
		return `{"action":"done","answer":"已搜索"}`, nil
	}
	if _, err := runBrowse(context.Background(), "搜索天气", driver, ask); err != nil {
		t.Fatal(err)
	}
	if len(driver.typed) != 1 || driver.typed[0] != "1:天气" {
		t.Fatalf("输入没有执行: %#v", driver.typed)
	}
}

func TestBrowseRetriesBadJSON(t *testing.T) {
	driver := &fakePage{shot: pageShot{Text: "今天晴"}}
	step := 0
	ask := func(context.Context, string, string) (string, error) {
		step++
		if step == 1 {
			return "我看看", nil
		}
		return `{"action":"done","answer":"晴"}`, nil
	}
	got, err := runBrowse(context.Background(), "天气", driver, ask)
	if err != nil {
		t.Fatal(err)
	}
	if got != "晴" {
		t.Fatalf("重试后应得到结论: %s", got)
	}
}

func TestFetchBrowseNeedsModel(t *testing.T) {
	t.Setenv("WEIXIN_AI_BASE", "")
	t.Setenv("WEIXIN_AI_KEY", "")
	t.Setenv("WEIXIN_AI_MODEL", "")
	_, err := FetchBrowse(context.Background(), "https://example.com\n总结", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "还没有配置模型") {
		t.Fatal(err)
	}
}

func TestBrowseTaskSendsAnswer(t *testing.T) {
	book := openTestBook(t)
	saved, err := book.Save(Task{
		Name: "分析", Account: "工作", Enabled: true,
		Kind: KindBrowse, Body: "https://example.com\n总结公告",
		Mode: ModeInterval, EveryMinutes: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	book.browse = func(context.Context, string) (string, error) {
		return "今天有两条公告", nil
	}
	var sent string
	_, err = book.RunOne(context.Background(), saved.ID, func(_ context.Context, _, _, body string) error {
		sent = body
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sent != "今天有两条公告" {
		t.Fatalf("应把结论发出去: %s", sent)
	}

	failed, err := book.Save(Task{
		Name: "失败", Account: "工作", Enabled: true,
		Kind: KindBrowse, Body: "https://example.com\n总结公告",
		Mode: ModeInterval, EveryMinutes: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	book.browse = func(context.Context, string) (string, error) {
		return "", errBrowseTimeout
	}
	sent = ""
	if _, err := book.RunOne(context.Background(), failed.ID, func(_ context.Context, _, _, body string) error {
		sent = body
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if sent != "浏览分析失败：超时" {
		t.Fatalf("失败说明不对: %s", sent)
	}
}

type fakePage struct {
	shot    pageShot
	clicks  []int
	typed   []string
	scrolls []string
}

func (f *fakePage) Snapshot(context.Context) (pageShot, error) { return f.shot, nil }

func (f *fakePage) Click(_ context.Context, index int) error {
	f.clicks = append(f.clicks, index)
	return nil
}

func (f *fakePage) Type(_ context.Context, index int, text string) error {
	f.typed = append(f.typed, fmt.Sprintf("%d:%s", index, text))
	return nil
}

func (f *fakePage) Scroll(_ context.Context, direction string) error {
	f.scrolls = append(f.scrolls, direction)
	return nil
}
