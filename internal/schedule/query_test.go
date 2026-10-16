package schedule

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAnswerRunsLiveInsteadOfLog(t *testing.T) {
	book := openTestBook(t)
	book.runCommand = func(context.Context, string) (string, error) {
		return "实时输出", nil
	}
	first, err := book.Save(Task{
		Name: "早报", Account: "工作", Code: "zaobao",
		Kind: KindText, Body: "早上好",
		ShowName: true,
		Mode:     ModeDaily, At: "08:30", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := book.Save(Task{
		Name: "探针", Account: "工作", Code: "1",
		Kind: KindCommand, Body: "echo hi",
		Mode: ModeDaily, At: "20:00", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	book.AddLog(RunLog{Time: time.Now(), TaskID: first.ID, Name: first.Name, Result: "已发送", Text: "旧的早报日志"})
	book.AddLog(RunLog{Time: time.Now(), TaskID: second.ID, Name: second.Name, Result: "已发送", Text: "curl：\ncurl example\n\n旧日志"})

	got, ok, err := book.Answer(context.Background(), "2")
	if err != nil || !ok || got != "实时输出" || strings.Contains(got, "旧日志") || strings.Contains(got, "curl") {
		t.Fatalf("序号应返回实时输出: ok=%v err=%v\n%s", ok, err, got)
	}
	got, ok, err = book.Answer(context.Background(), "1")
	if err != nil || !ok || got != "实时输出" {
		t.Fatalf("查询号应优先于序号: ok=%v err=%v\n%s", ok, err, got)
	}
	got, ok, err = book.Answer(context.Background(), "ZAOBAO")
	if err != nil || !ok || got != "任务: 早报\n早上好" {
		t.Fatalf("查询号应带上和自动发送一样的附带内容: ok=%v err=%v\n%s", ok, err, got)
	}
	got, ok, err = book.Answer(context.Background(), strings.ToUpper(first.ID))
	if err != nil || !ok || !strings.Contains(got, "早上好") || strings.Contains(got, "旧的早报日志") {
		t.Fatalf("任务编号: ok=%v err=%v\n%s", ok, err, got)
	}
	got, ok, err = book.Answer(context.Background(), "９")
	if err != nil || !ok || got != "没有编号 9 的定时任务" {
		t.Fatalf("全角序号: ok=%v err=%v\n%s", ok, err, got)
	}
	if _, ok, err = book.Answer(context.Background(), "你好"); err != nil || ok {
		t.Fatalf("普通消息不应当成查询: ok=%v err=%v", ok, err)
	}

	fresh, _, err := book.Get(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.LastRun.Equal(second.LastRun) || fresh.LastResult != "" {
		t.Fatalf("实时查询不应改掉定时记录: %+v", fresh)
	}
	logs, err := book.Logs()
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 || !strings.Contains(logs[0].Text, "旧日志") {
		t.Fatalf("实时查询不应改写日志: %+v", logs)
	}

	if _, err := book.Save(Task{
		Name: "空任务", Account: "工作",
		Kind: KindText, Body: "还没跑",
		Mode: ModeDaily, At: "09:00", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	got, ok, err = book.Answer(context.Background(), "3")
	if err != nil || !ok || got != "还没跑" {
		t.Fatalf("未跑过的任务也应返回当前正文: ok=%v err=%v\n%s", ok, err, got)
	}
	if label := TaskLabel(1, first); !strings.Contains(label, "1  zaobao  早报") {
		t.Fatalf("列表标签: %s", label)
	}
	if !strings.Contains(first.Detail(), "查询号 zaobao") {
		t.Fatalf("详情应带查询号: %s", first.Detail())
	}
}

func TestQueryCodeMustBeUnique(t *testing.T) {
	book := openTestBook(t)
	if _, err := book.Save(Task{
		Name: "早报", Account: "工作", Code: "zaobao",
		Kind: KindText, Body: "你好",
		Mode: ModeDaily, At: "08:30", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := book.Save(Task{
		Name: "晚报", Account: "工作", Code: "Zaobao",
		Kind: KindText, Body: "你好",
		Mode: ModeDaily, At: "20:00", Enabled: true,
	})
	if err == nil || !strings.Contains(err.Error(), "已被") {
		t.Fatalf("重复查询号应拒绝: %v", err)
	}
	_, err = book.Save(Task{
		Name: "晚报", Account: "工作", Code: "有 空格",
		Kind: KindText, Body: "你好",
		Mode: ModeDaily, At: "20:00", Enabled: true,
	})
	if err == nil || !strings.Contains(err.Error(), "空格") {
		t.Fatalf("带空格的查询号应拒绝: %v", err)
	}
}
