// Package tui 在终端里列出已登录微信和各自的发送状态。
package tui

import (
	"fmt"
	"strings"
	"time"
)

// Account 是界面上的一行微信。
type Account struct {
	Name     string
	Online   bool
	Owner    string
	Sessions []Session
}

// Session 是该微信下一条可发送会话。
type Session struct {
	User    string
	Detail  string
	Updated time.Time
}

// Plain 是不能进入全屏时的纯文本列表。
func Plain(accounts []Account) string {
	if len(accounts) == 0 {
		return "还没有登录微信。先执行 login -name 名称。\n"
	}
	var b strings.Builder
	for _, account := range accounts {
		fmt.Fprintf(&b, "%s\t%s\t扫码用户 %s\n", account.Name, loginText(account.Online), emptyDash(account.Owner))
		if len(account.Sessions) == 0 {
			b.WriteString("  还没有会话\n")
			continue
		}
		for _, session := range account.Sessions {
			fmt.Fprintf(&b, "  %s\n    %s\n    上次收到 %s\n", session.User, session.Detail, when(session.Updated))
		}
	}
	return b.String()
}

// View 是当前界面状态。
type View struct {
	Accounts  []Account
	Account   int
	Session   int
	Composing bool
	Draft     string
	Notice    string
}

// Render 画出全屏界面。
func Render(width, height int, view View) string {
	if width < 40 {
		width = 40
	}
	if height < 10 {
		height = 10
	}
	help := "↑↓ 微信    ←→ 会话    Enter 写消息    r 刷新    q 退出"
	if view.Composing {
		help = "输入消息    Enter 发送    Esc 取消    Backspace 删除"
	}
	lines := []string{
		"微信 ClawBot",
		help,
		"",
		fit("  名称", 18) + fit("登录", 14) + "会话",
	}
	accounts := view.Accounts
	if len(accounts) == 0 {
		lines = append(lines, "  还没有登录微信。先执行 login -name 名称。")
	}
	selected := view.Account
	if selected < 0 {
		selected = 0
	}
	if selected >= len(accounts) && len(accounts) > 0 {
		selected = len(accounts) - 1
	}
	for i, account := range accounts {
		mark := "  "
		if i == selected {
			mark = "> "
		}
		lines = append(lines, mark+fit(account.Name, 16)+fit(loginText(account.Online), 14)+fmt.Sprintf("%d", len(account.Sessions)))
	}
	lines = append(lines, "")
	if len(accounts) > 0 {
		current := accounts[selected]
		lines = append(lines, "会话 · "+current.Name)
		if len(current.Sessions) == 0 {
			lines = append(lines, "  还没有会话。先执行 serve，再让对方给这个 ClawBot 发一条消息。")
		} else {
			lines = append(lines, "  "+fit("用户", 36)+"说明")
			sessionIndex := view.Session
			if sessionIndex < 0 || sessionIndex >= len(current.Sessions) {
				sessionIndex = 0
			}
			for i, session := range current.Sessions {
				mark := "  "
				if i == sessionIndex {
					mark = "> "
				}
				lines = append(lines, mark+fit(session.User, 36)+session.Detail)
				lines = append(lines, "  "+fit("", 36)+"上次收到 "+when(session.Updated))
			}
		}
	}
	reserved := 1
	if view.Composing {
		reserved = 3
	}
	if len(lines) > height-reserved {
		lines = lines[:height-reserved]
	}
	if view.Notice != "" {
		lines = append(lines, view.Notice)
	} else {
		lines = append(lines, "")
	}
	if view.Composing {
		accountName := ""
		user := ""
		if selected < len(accounts) {
			accountName = accounts[selected].Name
			if view.Session >= 0 && view.Session < len(accounts[selected].Sessions) {
				user = accounts[selected].Sessions[view.Session].User
			}
		}
		lines = append(lines, "发给 "+accountName+" / "+user)
		lines = append(lines, "> "+tail(view.Draft, width-2))
	}
	return frame(width, height, lines)
}

func frame(width, height int, lines []string) string {
	if len(lines) > height {
		lines = lines[:height]
	}
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(clip(line, width))
		b.WriteByte('\n')
	}
	return b.String()
}

func loginText(online bool) string {
	if online {
		return "已登录"
	}
	return "登录已失效"
}

func when(t time.Time) string {
	if t.IsZero() {
		return "从未"
	}
	return t.Format("2006-01-02 15:04:05")
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func fit(s string, width int) string {
	s = clip(s, width)
	gap := width - displayWidth(s)
	if gap > 0 {
		s += strings.Repeat(" ", gap)
	}
	return s
}

func tail(s string, width int) string {
	if width <= 0 || displayWidth(s) <= width {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && displayWidth(string(runes)) > width {
		runes = runes[1:]
	}
	return string(runes)
}

func clip(s string, width int) string {
	if displayWidth(s) <= width {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := runeWidth(r)
		if used+w > width {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String()
}

func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

func runeWidth(r rune) int {
	if r == 0 || r == '\n' || r == '\r' {
		return 0
	}
	if r < 0x1100 {
		return 1
	}
	if (r >= 0x1100 && r <= 0x115F) ||
		(r >= 0x2E80 && r <= 0xA4CF) ||
		(r >= 0xAC00 && r <= 0xD7A3) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0xFE10 && r <= 0xFE6F) ||
		(r >= 0xFF00 && r <= 0xFF60) ||
		(r >= 0xFFE0 && r <= 0xFFE6) {
		return 2
	}
	return 1
}
