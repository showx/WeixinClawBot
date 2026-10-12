package schedule

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

const (
	tickEvery   = 15 * time.Second
	sendTimeout = 30 * time.Second
)

// SendFunc 把任务结果发到指定微信。to 为空表示该微信最近的会话。
type SendFunc func(ctx context.Context, account, to, text string) error

// Loop 每隔一会儿检查到期任务。ctx 结束时返回。
func (b *Book) Loop(ctx context.Context, send SendFunc) {
	ticker := time.NewTicker(tickEvery)
	defer ticker.Stop()
	b.tick(ctx, send)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.tick(ctx, send)
		}
	}
}

func (b *Book) tick(ctx context.Context, send SendFunc) {
	tasks, err := b.List()
	if err != nil {
		log.Printf("定时任务读取失败: %v", err)
		return
	}
	now := time.Now()
	for _, task := range tasks {
		if ctx.Err() != nil {
			return
		}
		if !task.Due(now) {
			continue
		}
		_, _ = b.run(ctx, task, send, false)
	}
}

// RunOne 立刻执行一条任务，用来在窗口里试跑。停用的任务也可以试跑。
func (b *Book) RunOne(ctx context.Context, id string, send SendFunc) (string, error) {
	task, ok, err := b.Get(id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("没有这个定时任务")
	}
	return b.run(ctx, task, send, true)
}

func (b *Book) run(ctx context.Context, task Task, send SendFunc, manual bool) (result string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("任务运行中断: %v", rec)
			b.noteCrash(rec)
		}
	}()
	if !b.claim(task.ID) {
		return "", errors.New("这个任务正在运行")
	}
	defer b.release(task.ID)

	fresh, ok, err := b.Get(task.ID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("没有这个定时任务")
	}
	if !manual && !fresh.Due(time.Now()) {
		return "", nil
	}

	text, result, shouldSend := b.produce(ctx, fresh)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if shouldSend {
		sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
		err = send(sendCtx, fresh.Account, fresh.To, text)
		cancel()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err != nil {
			result = trimResult("发送失败：" + err.Error())
		}
	}
	if markErr := b.Mark(fresh.ID, time.Now(), result); markErr != nil {
		log.Printf("定时任务 %s 没能记下结果: %v", fresh.Name, markErr)
	}
	if strings.Contains(result, "失败") {
		log.Printf("定时任务 %s：%s", fresh.Name, result)
	}
	if strings.HasPrefix(result, "发送失败") {
		return result, errors.New(result)
	}
	return result, nil
}

func (b *Book) noteCrash(rec any) {
	if b == nil || b.path == "" {
		return
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(b.path), "task-panic.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %v\n%s\n", time.Now().Format(time.RFC3339), rec, debug.Stack())
}

func (b *Book) produce(ctx context.Context, task Task) (text, result string, shouldSend bool) {
	if task.Kind == KindPage {
		fetch := b.fetchPage
		if fetch == nil {
			fetch = FetchPage
		}
		output, err := fetch(ctx, task.Body)
		output = strings.TrimSpace(output)
		if err != nil {
			msg := "页面抓取失败：" + pageFailure(err)
			text, truncated := truncateRunes(msg, sendRuneLimit)
			result = "已发送页面失败说明"
			if truncated {
				result = "已发送页面失败说明（已截断）"
			}
			return text, result, true
		}
		if output == "" {
			return "", "页面没有识别出文字，未发送", false
		}
		text, truncated := truncateRunes(output, 8000)
		result = "已发送"
		if truncated {
			result = "已发送（输出已截断）"
		}
		return text, result, true
	}
	if task.Kind == KindCommand {
		run := b.runCommand
		if run == nil {
			run = RunCommand
		}
		output, err := run(ctx, task.Body)
		output = strings.TrimSpace(output)
		if err != nil {
			msg := commandFailure(output, err)
			text, truncated := truncateRunes(msg, sendRuneLimit)
			result = "已发送命令失败说明"
			if truncated {
				result = "已发送命令失败说明（已截断）"
			}
			return text, result, true
		}
		if output == "" {
			return "", "命令没有输出，未发送", false
		}
		text, truncated := truncateRunes(output, sendRuneLimit)
		result = "已发送"
		if truncated {
			result = "已发送（输出已截断）"
		}
		return text, result, true
	}

	body := strings.TrimSpace(task.Body)
	if body == "" {
		return "", "没有内容，未发送", false
	}
	text, truncated := truncateRunes(body, sendRuneLimit)
	result = "已发送"
	if truncated {
		result = "已发送（内容已截断）"
	}
	return text, result, true
}

func pageFailure(err error) string {
	if errors.Is(err, errPageTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return "超时"
	}
	return err.Error()
}

func commandFailure(output string, err error) string {
	reason := err.Error()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		reason = fmt.Sprintf("退出码 %d", exit.ExitCode())
	}
	if errors.Is(err, errCommandTimeout) {
		reason = "超时"
	}
	if output == "" {
		return "命令失败：" + reason
	}
	return output + "\n\n命令失败：" + reason
}
