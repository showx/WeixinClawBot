package tui

import (
	"strings"
	"testing"
	"time"
)

func TestRenderListsAccountsAndSessions(t *testing.T) {
	accounts := []Account{
		{
			Name:   "工作",
			Online: true,
			Owner:  "work@im.wechat",
			Sessions: []Session{{
				User:    "u@im.wechat",
				Detail:  "可尝试发送，自上次收到消息后已发 1 条",
				Updated: time.Date(2026, 10, 10, 10, 0, 0, 0, time.Local),
			}},
		},
		{Name: "生活", Online: false},
	}
	screen := Render(80, 24, View{Accounts: accounts})
	for _, want := range []string{"> 工作", "已登录", "生活", "登录已失效", "u@im.wechat", "可尝试发送", "2026-10-10 10:00:00"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("界面缺少 %q\n%s", want, screen)
		}
	}
	other := Render(80, 24, View{Accounts: accounts, Account: 1})
	if !strings.Contains(other, "> 生活") || !strings.Contains(other, "还没有会话") {
		t.Fatalf("第二个微信没有被选中\n%s", other)
	}
}

func TestRenderComposer(t *testing.T) {
	accounts := []Account{{
		Name:   "工作",
		Online: true,
		Sessions: []Session{
			{User: "a@im.wechat", Detail: "可尝试发送，自上次收到消息后已发 0 条"},
			{User: "b@im.wechat", Detail: "可尝试发送，自上次收到消息后已发 2 条"},
		},
	}}
	screen := Render(80, 24, View{Accounts: accounts, Session: 1, Composing: true, Draft: "测试一下"})
	for _, want := range []string{"> b@im.wechat", "发给 工作 / b@im.wechat", "> 测试一下", "Enter 发送"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("发送界面缺少 %q\n%s", want, screen)
		}
	}
}

func TestPlainEmpty(t *testing.T) {
	if !strings.Contains(Plain(nil), "还没有登录微信") {
		t.Fatal(Plain(nil))
	}
}
