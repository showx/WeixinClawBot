package schedule

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunLogRecordsSend(t *testing.T) {
	book := openTestBook(t)
	saved, err := book.Save(Task{
		Name: "早报", Account: "工作", To: "张三",
		Kind: KindText, Body: "你好",
		Mode: ModeDaily, At: "08:30", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := book.RunOne(context.Background(), saved.ID, func(_ context.Context, account, to, text string) error {
		if account != "工作" || to != "张三" || text != "你好" {
			t.Fatalf("发出的内容不对: %s %s %s", account, to, text)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "已发送") {
		t.Fatalf("结果: %s", result)
	}
	logs, err := book.Logs()
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].Text != "你好" || !logs[0].Manual || logs[0].Name != "早报" {
		t.Fatalf("日志不对: %+v", logs)
	}
	if !strings.Contains(logs[0].Line(), "早报") || !strings.Contains(logs[0].Detail(), "你好") || !strings.Contains(logs[0].Detail(), "张三") {
		t.Fatalf("展示不对:\n%s\n%s", logs[0].Line(), logs[0].Detail())
	}

	_, err = book.RunOne(context.Background(), saved.ID, func(context.Context, string, string, string) error {
		return errors.New("拒收")
	})
	if err == nil || !strings.Contains(err.Error(), "发送失败") {
		t.Fatalf("发送失败应返回错误: %v", err)
	}
	logs, err = book.Logs()
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 || !strings.Contains(logs[0].Result, "拒收") || !strings.Contains(logs[0].Text, "你好") || !strings.Contains(logs[0].Detail(), "拒收") {
		t.Fatalf("失败记录应留在最前，并保留当时的内容: %+v", logs[0])
	}

	book.AddPhone("工作", "u@im.wechat", "手机上的话")
	logs, err = book.Logs()
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 3 || !logs[0].Phone || !strings.Contains(logs[0].Line(), "手机") || !strings.Contains(logs[0].Line(), "手机上的话") || !strings.Contains(logs[0].Detail(), "手机上的话") {
		t.Fatalf("手机消息应出现在最前: %+v\n%s", logs[0], logs[0].Detail())
	}

	book.AddLog(RunLog{Name: "即时发送", Account: "工作", To: "李四", Result: "已发送（1 条）", Text: "在吗", Live: true})
	logs, err = book.Logs()
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 4 || !logs[0].Live || !strings.Contains(logs[0].Line(), "即时") || !strings.Contains(logs[0].Detail(), "在吗") {
		t.Fatalf("即时发送应出现在最前: %+v", logs[0])
	}
}
