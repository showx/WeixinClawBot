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
	KindAPI      = "api"
	KindBrowse   = "browse"
	ModeDaily    = "daily"
	ModeInterval = "interval"

	maxTextRunes    = 8000
	maxCommandRunes = 2000
	sendRuneLimit   = 4000
)

// Task 是一条定时任务。接收者为空时，发给该微信最近的会话。
type Task struct {
	ID           string    `json:"id"`
	Code         string    `json:"code,omitempty"`
	Name         string    `json:"name"`
	Account      string    `json:"account"`
	Accounts     []string  `json:"accounts,omitempty"`
	To           string    `json:"to,omitempty"`
	Enabled      bool      `json:"enabled"`
	Kind         string    `json:"kind"`
	Body         string    `json:"body"`
	Mode         string    `json:"mode"`
	At           string    `json:"at,omitempty"`
	EveryMinutes int       `json:"every_minutes,omitempty"`
	ShowTime     bool      `json:"show_time,omitempty"`
	ShowName     bool      `json:"show_name,omitempty"`
	Prefix       string    `json:"prefix,omitempty"`
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
	names := t.AccountNames()
	if len(names) == 0 {
		return fmt.Errorf("请选择微信")
	}
	if len(names) > 20 {
		return fmt.Errorf("一个任务最多选择 20 个微信")
	}
	for _, account := range names {
		if utf8.RuneCountInString(account) > 32 || strings.ContainsAny(account, `/\`) {
			return fmt.Errorf("微信名称不能包含斜杠，最多 32 个字")
		}
	}
	if utf8.RuneCountInString(strings.TrimSpace(t.To)) > 128 {
		return fmt.Errorf("接收者太长")
	}
	if utf8.RuneCountInString(strings.TrimSpace(t.Prefix)) > 200 {
		return fmt.Errorf("附言最多 200 个字")
	}
	code := strings.TrimSpace(t.Code)
	if utf8.RuneCountInString(code) > 32 {
		return fmt.Errorf("查询号最多 32 个字")
	}
	if strings.ContainsAny(code, " \t\r\n") {
		return fmt.Errorf("查询号不能包含空格")
	}
	switch t.Kind {
	case KindText, KindCommand, KindPage, KindAPI, KindBrowse:
	default:
		return fmt.Errorf("请选择固定文本、运行命令、抓取页面、接口数据或浏览分析")
	}
	body := strings.TrimSpace(t.Body)
	if body == "" {
		switch t.Kind {
		case KindPage:
			return fmt.Errorf("请填写页面网址")
		case KindAPI:
			return fmt.Errorf("请填写接口地址和字段")
		case KindBrowse:
			return fmt.Errorf("请填写页面网址和要分析的问题")
		default:
			return fmt.Errorf("请填写要发送的内容")
		}
	}
	limit := maxTextRunes
	switch t.Kind {
	case KindCommand:
		limit = maxCommandRunes
	case KindPage, KindAPI, KindBrowse:
		limit = 4000
	}
	if utf8.RuneCountInString(body) > limit {
		switch t.Kind {
		case KindPage:
			return fmt.Errorf("网址和 Cookie 最多 %d 个字", limit)
		case KindAPI:
			return fmt.Errorf("接口地址和字段最多 %d 个字", limit)
		case KindBrowse:
			return fmt.Errorf("网址和问题最多 %d 个字", limit)
		default:
			return fmt.Errorf("内容最多 %d 个字", limit)
		}
	}
	if t.Kind == KindPage {
		if _, err := parsePageBody(body); err != nil {
			return err
		}
	}
	if t.Kind == KindBrowse {
		if _, err := parseBrowseBody(body); err != nil {
			return err
		}
	}
	if t.Kind == KindAPI {
		if _, err := parseAPIBody(body); err != nil {
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
	return fmt.Sprintf("%s%s  %s  %s → %s", t.Name, state, when, t.AccountLabel(), target)
}

// TaskLabel 是任务列表里的一行，前面带从 1 开始的序号。有查询号时写在序号后面。
func TaskLabel(n int, task Task) string {
	if code := strings.TrimSpace(task.Code); code != "" {
		return fmt.Sprintf("%d  %s  %s", n, code, task.Summary())
	}
	return fmt.Sprintf("%d  %s", n, task.Summary())
}

// AccountNames 是这个任务要发送的微信。旧任务只写了 account 时仍然有效。
func (t Task) AccountNames() []string {
	seen := map[string]bool{}
	var names []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	for _, name := range t.Accounts {
		add(name)
	}
	if len(names) == 0 {
		add(t.Account)
	}
	return names
}

// Copy 复制一条任务，名称加上「副本」，清空运行记录。保存后才写进列表。
func (t Task) Copy() Task {
	if t.Accounts != nil {
		t.Accounts = append([]string(nil), t.Accounts...)
	}
	t.ID = ""
	t.Code = ""
	t.LastRun = time.Time{}
	t.LastResult = ""
	t.Name = copyName(t.Name)
	return t
}

func copyName(name string) string {
	name = strings.TrimSpace(name)
	const suffix = " 副本"
	if name == "" {
		return "副本"
	}
	if !strings.HasSuffix(name, suffix) {
		name += suffix
	}
	if n := utf8.RuneCountInString(name); n > 32 {
		keep := 32 - utf8.RuneCountInString(suffix)
		name = string([]rune(name)[:keep]) + suffix
	}
	return name
}

// AccountLabel 用顿号把多个微信拼成一行。
func (t Task) AccountLabel() string {
	names := t.AccountNames()
	if len(names) == 0 {
		return ""
	}
	return strings.Join(names, "、")
}

func (t Task) Detail() string {
	kind := "固定文本"
	switch t.Kind {
	case KindCommand:
		kind = "运行命令"
	case KindPage:
		kind = "抓取页面"
	case KindAPI:
		kind = "接口数据"
	case KindBrowse:
		kind = "浏览分析"
	}
	when := "还没有运行"
	if !t.LastRun.IsZero() {
		when = "上次 " + t.LastRun.Format("2006-01-02 15:04")
	}
	result := t.LastResult
	if result == "" {
		result = "无结果"
	}
	line := kind + "。" + when + "：" + result
	if code := strings.TrimSpace(t.Code); code != "" {
		line += "。查询号 " + code
	}
	return line
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

// Decorate 把勾选的附言、发送时间和任务名称写在正文前面。
func (t Task) Decorate(body string, now time.Time) string {
	return DecorateMessage(MessagePreface(t.Name, t.ShowTime, t.ShowName, t.Prefix, now), body)
}

// MessagePreface 是勾选后要加在正文前面的几行。
func MessagePreface(name string, showTime, showName bool, prefix string, now time.Time) string {
	var lines []string
	if text := strings.TrimSpace(prefix); text != "" {
		lines = append(lines, text)
	}
	if showTime && !now.IsZero() {
		lines = append(lines, "时间: "+now.Format("2006-01-02 15:04"))
	}
	if showName {
		if name = strings.TrimSpace(name); name != "" {
			lines = append(lines, "任务: "+name)
		}
	}
	return strings.Join(lines, "\n")
}

// DecorateMessage 把附带内容写在正文前面。
func DecorateMessage(preface, body string) string {
	preface = strings.TrimSpace(preface)
	body = strings.TrimSpace(body)
	if preface == "" {
		return body
	}
	if body == "" {
		return preface
	}
	return preface + "\n" + body
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
