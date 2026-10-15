package schedule

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const logKeep = 200

// RunLog 是一次定时任务运行的记录，包括实际发给微信的内容。
type RunLog struct {
	Time    time.Time `json:"time"`
	TaskID  string    `json:"task_id,omitempty"`
	Name    string    `json:"name"`
	Account string    `json:"account,omitempty"`
	To      string    `json:"to,omitempty"`
	Result  string    `json:"result"`
	Text    string    `json:"text,omitempty"`
	Manual  bool      `json:"manual,omitempty"`
	Live    bool      `json:"live,omitempty"`
	Phone   bool      `json:"phone,omitempty"`
}

func (b *Book) logPath() string {
	if b == nil || b.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(b.path), "task-log.json")
}

// Logs 返回最近的运行记录，新的在前面。
func (b *Book) Logs() ([]RunLog, error) {
	if b == nil || b.logPath() == "" {
		return nil, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.readLogs()
}

func (b *Book) recordRun(task Task, when time.Time, result, text string, manual bool) {
	b.AddLog(RunLog{
		Time:    when,
		TaskID:  task.ID,
		Name:    task.Name,
		Account: task.Account,
		To:      task.To,
		Result:  result,
		Text:    text,
		Manual:  manual,
	})
}

// AddPhone 记下对方从手机发给 ClawBot 的内容。
func (b *Book) AddPhone(account, user, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	name := strings.TrimSpace(user)
	if name == "" {
		name = "手机"
	}
	b.AddLog(RunLog{
		Name:    name,
		Account: strings.TrimSpace(account),
		To:      strings.TrimSpace(user),
		Result:  text,
		Text:    text,
		Phone:   true,
	})
}

// AddLog 记一条发送记录。主窗口的即时发送和定时任务共用这一份。
func (b *Book) AddLog(entry RunLog) {
	if b == nil || b.logPath() == "" {
		return
	}
	if entry.Time.IsZero() {
		entry.Time = time.Now()
	}
	entry.Result = strings.TrimSpace(entry.Result)
	if entry.Result == "" {
		entry.Result = "已运行"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	logs, err := b.readLogs()
	if err != nil {
		logs = nil
	}
	logs = append([]RunLog{entry}, logs...)
	if len(logs) > logKeep {
		logs = logs[:logKeep]
	}
	if err := b.writeLogs(logs); err != nil {
		fmt.Fprintf(os.Stderr, "发送日志没能保存: %v\n", err)
	}
}

func (b *Book) readLogs() ([]RunLog, error) {
	raw, err := os.ReadFile(b.logPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var stored struct {
		Entries []RunLog `json:"entries"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, err
	}
	return stored.Entries, nil
}

func (b *Book) writeLogs(logs []RunLog) error {
	if logs == nil {
		logs = []RunLog{}
	}
	raw, err := json.MarshalIndent(struct {
		Entries []RunLog `json:"entries"`
	}{Entries: logs}, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	path := b.logPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err == nil {
		return nil
	}
	_ = os.Remove(path)
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Kind 是这条记录从哪里来的。
func (e RunLog) Kind() string {
	switch {
	case e.Phone:
		return "手机"
	case e.Live:
		return "即时"
	case e.Manual:
		return "试跑"
	default:
		return "定时"
	}
}

// Line 是日志列表里的一行。
func (e RunLog) Line() string {
	when := e.Time.Local().Format("01-02 15:04")
	return when + "  " + e.Kind() + " · " + logName(e) + "  " + oneLine(e.Result, 48)
}

func logName(e RunLog) string {
	name := strings.TrimSpace(e.Name)
	if name == "" {
		return "未命名"
	}
	return name
}

func logTo(e RunLog) string {
	to := strings.TrimSpace(e.To)
	if to == "" {
		return "最近会话"
	}
	return to
}

// Detail 是选中一条日志后看到的全文。
func (e RunLog) Detail() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s    %s\n", e.Time.Local().Format("2006-01-02 15:04:05"), e.Kind())
	if e.Phone {
		if account := strings.TrimSpace(e.Account); account != "" {
			fmt.Fprintf(&b, "微信    %s\n", account)
		}
		fmt.Fprintf(&b, "来自    %s\n", logTo(e))
		writeLogBody(&b, "消息", e.Text)
		return b.String()
	}
	fmt.Fprintf(&b, "任务    %s\n", logName(e))
	if account := strings.TrimSpace(e.Account); account != "" {
		fmt.Fprintf(&b, "微信    %s\n", account)
	}
	fmt.Fprintf(&b, "发给    %s\n", logTo(e))
	fmt.Fprintf(&b, "结果    %s\n", strings.TrimSpace(e.Result))
	if strings.TrimSpace(e.Text) == "" {
		b.WriteString("\n这次没有发出内容。")
		return b.String()
	}
	writeLogBody(&b, "内容", e.Text)
	return b.String()
}

func writeLogBody(b *strings.Builder, title, text string) {
	b.WriteString("\n")
	b.WriteString(title)
	b.WriteString("\n")
	b.WriteString(strings.Repeat("—", 28))
	b.WriteString("\n")
	b.WriteString(strings.TrimSpace(text))
}
