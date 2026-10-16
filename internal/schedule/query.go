package schedule

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Answer 在对方发来任务序号、查询号或任务编号时，当场重跑并返回这次的正文。
// 正文和到点自动发送的内容相同，不读运行日志，也不改上次运行时间。
// 普通聊天返回 ok=false。查询号和序号写成一样时，以查询号为准。
func (b *Book) Answer(ctx context.Context, text string) (string, bool, error) {
	task, note, ok, err := b.FindQuery(text)
	if err != nil || !ok {
		return "", false, err
	}
	if note != "" {
		return note, true, nil
	}
	reply, err := b.LiveText(ctx, task.ID)
	return reply, true, err
}

// FindQuery 按查询号、任务编号、列表序号查找。note 非空时不用再运行。
func (b *Book) FindQuery(text string) (Task, string, bool, error) {
	text = normalizeQuery(text)
	if text == "" || b == nil {
		return Task{}, "", false, nil
	}
	tasks, err := b.List()
	if err != nil {
		return Task{}, "", false, err
	}
	if task, ok := matchCode(tasks, text); ok {
		return task, "", true, nil
	}
	if task, ok := matchID(tasks, text); ok {
		return task, "", true, nil
	}
	if n, ok := taskNumber(text); ok {
		if n < 1 || n > len(tasks) {
			return Task{}, fmt.Sprintf("没有编号 %d 的定时任务", n), true, nil
		}
		return tasks[n-1], "", true, nil
	}
	return Task{}, "", false, nil
}

// LiveText 立刻生成这条任务此刻要发送的正文。
func (b *Book) LiveText(ctx context.Context, id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("没有这个定时任务")
	}
	if !b.claim(id) {
		return "这个任务正在运行", nil
	}
	defer b.release(id)

	task, ok, err := b.Get(id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "没有这个定时任务", nil
	}
	task, err = b.applyMacros(task)
	if err != nil {
		return "", err
	}
	text, result, shouldSend := b.produce(ctx, task)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if !shouldSend {
		if strings.TrimSpace(result) == "" {
			return "这次没有内容", nil
		}
		return result, nil
	}
	text = task.Decorate(text, time.Now())
	text, _ = truncateRunes(text, sendRuneLimit)
	if strings.TrimSpace(text) == "" {
		return "这次没有内容", nil
	}
	return text, nil
}

func matchCode(tasks []Task, text string) (Task, bool) {
	for _, task := range tasks {
		code := normalizeQuery(task.Code)
		if code != "" && strings.EqualFold(code, text) {
			return task, true
		}
	}
	return Task{}, false
}

func matchID(tasks []Task, text string) (Task, bool) {
	for _, task := range tasks {
		if strings.EqualFold(strings.TrimSpace(task.ID), text) {
			return task, true
		}
	}
	return Task{}, false
}

func taskNumber(text string) (int, bool) {
	if text == "" || (len(text) > 1 && text[0] == '0') {
		return 0, false
	}
	n := 0
	for _, r := range text {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
		if n > 100000 {
			return 0, false
		}
	}
	return n, true
}

func normalizeQuery(text string) string {
	text = strings.TrimSpace(text)
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if r >= '０' && r <= '９' {
			r = '0' + (r - '０')
		}
		b.WriteRune(r)
	}
	return b.String()
}
