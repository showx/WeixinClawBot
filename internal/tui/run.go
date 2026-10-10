package tui

import (
	"fmt"
	"os"
	"time"
)

type key int

const (
	keyNone key = iota
	keyUp
	keyDown
	keyLeft
	keyRight
	keyQuit
	keyEnter
	keyBackspace
	keyRune
)

type press struct {
	kind key
	char rune
}

// Run 在终端全屏列出微信，并可给当前会话发一条测试消息。
// 标准输出不是控制台时，改为打印一次纯文本列表。
func Run(load func() ([]Account, error), send func(account, to, text string) (string, error)) error {
	accounts, err := load()
	if err != nil {
		return err
	}
	if !interactive() {
		fmt.Fprint(os.Stdout, Plain(accounts))
		return nil
	}
	enableVT()
	defer fmt.Fprint(os.Stdout, "\x1b[?25h")

	view := View{Accounts: accounts}
	draw := func(current View) {
		current = clampView(current)
		width, height := windowSize()
		if current.Composing {
			fmt.Fprint(os.Stdout, "\x1b[?25h")
		} else {
			fmt.Fprint(os.Stdout, "\x1b[?25l")
		}
		fmt.Fprint(os.Stdout, "\x1b[2J\x1b[H")
		fmt.Fprint(os.Stdout, Render(width, height, current))
	}
	for {
		view = clampView(view)
		draw(view)
		pressed, err := readKey(time.Second)
		if err != nil {
			return err
		}
		switch pressed.kind {
		case keyQuit:
			if view.Composing {
				view.Composing = false
				view.Draft = ""
				continue
			}
			return nil
		case keyUp:
			if !view.Composing && view.Account > 0 {
				view.Account--
				view.Session = 0
			}
		case keyDown:
			if !view.Composing && view.Account < len(view.Accounts)-1 {
				view.Account++
				view.Session = 0
			}
		case keyLeft:
			if !view.Composing && view.Session > 0 {
				view.Session--
			}
		case keyRight:
			if !view.Composing && view.Session < sessionCount(view)-1 {
				view.Session++
			}
		case keyEnter:
			if view.Composing && stringsTrim(view.Draft) != "" {
				busy := view
				busy.Notice = "正在发送..."
				busy.Composing = false
				draw(busy)
			}
			view, err = onEnter(view, load, send)
			if err != nil {
				return err
			}
		case keyBackspace:
			if view.Composing && view.Draft != "" {
				runes := []rune(view.Draft)
				view.Draft = string(runes[:len(runes)-1])
			}
		case keyRune:
			if view.Composing {
				view.Draft += string(pressed.char)
				continue
			}
			switch pressed.char {
			case 'q', 'Q':
				return nil
			case 'r', 'R':
				view.Accounts, err = load()
				if err != nil {
					return err
				}
			case 'k', 'K':
				if view.Account > 0 {
					view.Account--
					view.Session = 0
				}
			case 'j', 'J':
				if view.Account < len(view.Accounts)-1 {
					view.Account++
					view.Session = 0
				}
			}
		case keyNone:
			view.Accounts, err = load()
			if err != nil {
				return err
			}
		}
	}
}

func onEnter(view View, load func() ([]Account, error), send func(account, to, text string) (string, error)) (View, error) {
	if !view.Composing {
		account, user, ok := target(view)
		if !ok {
			view.Notice = "还没有可发送的会话。先执行 serve，再让对方给这个 ClawBot 发一条消息。"
			return view, nil
		}
		view.Composing = true
		view.Draft = ""
		view.Notice = "正在给 " + account + " 的会话 " + user + " 写消息"
		return view, nil
	}
	text := stringsTrim(view.Draft)
	if text == "" {
		view.Notice = "消息内容不能为空"
		return view, nil
	}
	account, user, ok := target(view)
	if !ok {
		view.Composing = false
		view.Notice = "会话已经不在了，请重新选择"
		return view, nil
	}
	view.Notice = "正在发送..."
	view.Composing = false
	msg, err := send(account, user, text)
	view.Draft = ""
	if err != nil {
		view.Notice = err.Error()
	} else {
		view.Notice = msg
	}
	accounts, loadErr := load()
	if loadErr != nil {
		return view, loadErr
	}
	view.Accounts = accounts
	return view, nil
}

func target(view View) (string, string, bool) {
	if view.Account < 0 || view.Account >= len(view.Accounts) {
		return "", "", false
	}
	account := view.Accounts[view.Account]
	if view.Session < 0 || view.Session >= len(account.Sessions) {
		return account.Name, "", false
	}
	return account.Name, account.Sessions[view.Session].User, true
}

func sessionCount(view View) int {
	if view.Account < 0 || view.Account >= len(view.Accounts) {
		return 0
	}
	return len(view.Accounts[view.Account].Sessions)
}

func clampView(view View) View {
	if view.Account >= len(view.Accounts) {
		view.Account = len(view.Accounts) - 1
	}
	if view.Account < 0 {
		view.Account = 0
	}
	if view.Session >= sessionCount(view) {
		view.Session = sessionCount(view) - 1
	}
	if view.Session < 0 {
		view.Session = 0
	}
	return view
}

func stringsTrim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
