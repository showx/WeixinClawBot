package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	KindText     = "text"
	KindCommand  = "command"
	KindPage     = "page"
	ModeDaily    = "daily"
	ModeInterval = "interval"

	maxTextRunes    = 8000
	maxCommandRunes = 2000
	sendRuneLimit   = 4000
)

// Task 是一条定时任务。接收者为空时，发给该微信最近的会话。
type Task struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Account      string    `json:"account"`
	To           string    `json:"to,omitempty"`
	Enabled      bool      `json:"enabled"`
	Kind         string    `json:"kind"`
	Body         string    `json:"body"`
	Mode         string    `json:"mode"`
	At           string    `json:"at,omitempty"`
	EveryMinutes int       `json:"every_minutes,omitempty"`
	LastRun      time.Time `json:"last_run,omitempty"`
	LastResult   string    `json:"last_result,omitempty"`
}

// Due 判断现在是否该跑。每天的任务只补今天这一档，不会把错过的每一天都补上。
// 新建时 LastRun 会被写成当前时间，所以刚建好的任务不会立刻执行。
func (t Task) Due(now time.Time) bool {
	if !t.Enabled {
		return false
	}
	switch t.Mode {
	case ModeDaily:
		slot, ok := todaySlot(now, t.At)
		if !ok || now.Before(slot) {
			return false
		}
		return t.LastRun.Before(slot)
	case ModeInterval:
		if t.EveryMinutes < 1 {
			return false
		}
		if t.LastRun.IsZero() {
			return true
		}
		return now.Sub(t.LastRun) >= time.Duration(t.EveryMinutes)*time.Minute
	default:
		return false
	}
}

func (t Task) Validate() error {
	name := strings.TrimSpace(t.Name)
	if name == "" {
		return fmt.Errorf("请填写任务名称")
	}
	if utf8.RuneCountInString(name) > 32 {
		return fmt.Errorf("任务名称最多 32 个字")
	}
	account := strings.TrimSpace(t.Account)
	if account == "" {
		return fmt.Errorf("请选择微信")
	}
	if utf8.RuneCountInString(account) > 32 || strings.ContainsAny(account, `/\`) {
		return fmt.Errorf("微信名称不能包含斜杠，最多 32 个字")
	}
	if utf8.RuneCountInString(strings.TrimSpace(t.To)) > 128 {
		return fmt.Errorf("接收者太长")
	}
	switch t.Kind {
	case KindText, KindCommand, KindPage:
	default:
		return fmt.Errorf("请选择固定文本、运行命令或抓取页面")
	}
	body := strings.TrimSpace(t.Body)
	if body == "" {
		if t.Kind == KindPage {
			return fmt.Errorf("请填写页面网址")
		}
		return fmt.Errorf("请填写要发送的内容")
	}
	limit := maxTextRunes
	switch t.Kind {
	case KindCommand:
		limit = maxCommandRunes
	case KindPage:
		limit = 4000
	}
	if utf8.RuneCountInString(body) > limit {
		if t.Kind == KindPage {
			return fmt.Errorf("网址和 Cookie 最多 %d 个字", limit)
		}
		return fmt.Errorf("内容最多 %d 个字", limit)
	}
	if t.Kind == KindPage {
		if _, err := parsePageBody(body); err != nil {
			return err
		}
	}
	switch t.Mode {
	case ModeDaily:
		if _, _, ok := parseClock(t.At); !ok {
			return fmt.Errorf("时间写成 08:30 这种格式")
		}
	case ModeInterval:
		if t.EveryMinutes < 1 || t.EveryMinutes > 7*24*60 {
			return fmt.Errorf("间隔要在 1 到 10080 分钟之间")
		}
	default:
		return fmt.Errorf("请选择每天或每隔")
	}
	return nil
}

func (t Task) Summary() string {
	when := "每天 " + t.At
	if t.Mode == ModeInterval {
		when = fmt.Sprintf("每 %d 分钟", t.EveryMinutes)
	}
	state := ""
	if !t.Enabled {
		state = "（停用）"
	}
	target := t.To
	if target == "" {
		target = "最近会话"
	}
	return fmt.Sprintf("%s%s  %s  %s → %s", t.Name, state, when, t.Account, target)
}

func (t Task) Detail() string {
	kind := "固定文本"
	switch t.Kind {
	case KindCommand:
		kind = "运行命令"
	case KindPage:
		kind = "抓取页面"
	}
	when := "还没有运行"
	if !t.LastRun.IsZero() {
		when = "上次 " + t.LastRun.Format("2006-01-02 15:04")
	}
	result := t.LastResult
	if result == "" {
		result = "无结果"
	}
	return kind + "。" + when + "：" + result
}

func todaySlot(now time.Time, at string) (time.Time, bool) {
	hour, minute, ok := parseClock(at)
	if !ok {
		return time.Time{}, false
	}
	year, month, day := now.Date()
	return time.Date(year, month, day, hour, minute, 0, 0, now.Location()), true
}

func parseClock(at string) (int, int, bool) {
	parts := strings.Split(strings.TrimSpace(at), ":")
	if len(parts) != 2 || len(parts[1]) != 2 {
		return 0, 0, false
	}
	hour, errH := strconv.Atoi(parts[0])
	minute, errM := strconv.Atoi(parts[1])
	if errH != nil || errM != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, false
	}
	return hour, minute, true
}

func normalizeClock(at string) (string, bool) {
	hour, minute, ok := parseClock(at)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%02d:%02d", hour, minute), true
}

func truncateRunes(s string, limit int) (string, bool) {
	runes := []rune(s)
	if len(runes) <= limit {
		return s, false
	}
	if limit < 1 {
		return "", true
	}
	return string(runes[:limit-1]) + "…", true
}
