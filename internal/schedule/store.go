package schedule

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Book 把定时任务放在数据目录的 schedules.json。
type Book struct {
	mu         sync.Mutex
	path       string
	running    map[string]bool
	runCommand func(ctx context.Context, command string) (string, error)
	fetchPage  func(ctx context.Context, rawURL string) (string, error)
}

type file struct {
	Tasks []Task `json:"tasks"`
}

func Open(dir string) (*Book, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Book{
		path:    filepath.Join(dir, "schedules.json"),
		running: map[string]bool{},
	}, nil
}

func (b *Book) List() ([]Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	stored, err := b.read()
	if err != nil {
		return nil, err
	}
	return append([]Task(nil), stored.Tasks...), nil
}

func (b *Book) Get(id string) (Task, bool, error) {
	list, err := b.List()
	if err != nil {
		return Task{}, false, err
	}
	for _, task := range list {
		if task.ID == id {
			return task, true, nil
		}
	}
	return Task{}, false, nil
}

// Save 新建或更新任务。新建时把上次运行记成现在，避免一保存就发送。
// 更新时保留上次运行时间和结果。
func (b *Book) Save(task Task) (Task, error) {
	task.Name = strings.TrimSpace(task.Name)
	task.Account = strings.TrimSpace(task.Account)
	task.To = strings.TrimSpace(task.To)
	task.Body = strings.TrimSpace(task.Body)
	task.At = strings.TrimSpace(task.At)
	if task.Mode == ModeDaily {
		if at, ok := normalizeClock(task.At); ok {
			task.At = at
		}
		task.EveryMinutes = 0
	} else if task.Mode == ModeInterval {
		task.At = ""
	}
	if err := task.Validate(); err != nil {
		return Task{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	stored, err := b.read()
	if err != nil {
		return Task{}, err
	}
	if task.ID == "" {
		task.ID = newID()
		task.LastRun = time.Now()
		task.LastResult = ""
		stored.Tasks = append(stored.Tasks, task)
	} else {
		found := false
		for i, old := range stored.Tasks {
			if old.ID != task.ID {
				continue
			}
			task.LastRun = old.LastRun
			task.LastResult = old.LastResult
			stored.Tasks[i] = task
			found = true
			break
		}
		if !found {
			return Task{}, errors.New("没有这个定时任务")
		}
	}
	if err := b.write(stored); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (b *Book) Delete(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	stored, err := b.read()
	if err != nil {
		return err
	}
	next := make([]Task, 0, len(stored.Tasks))
	found := false
	for _, task := range stored.Tasks {
		if task.ID == id {
			found = true
			continue
		}
		next = append(next, task)
	}
	if !found {
		return errors.New("没有这个定时任务")
	}
	stored.Tasks = next
	return b.write(stored)
}

func (b *Book) Mark(id string, when time.Time, result string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	stored, err := b.read()
	if err != nil {
		return err
	}
	for i := range stored.Tasks {
		if stored.Tasks[i].ID != id {
			continue
		}
		stored.Tasks[i].LastRun = when
		stored.Tasks[i].LastResult = trimResult(result)
		return b.write(stored)
	}
	return nil
}

func (b *Book) read() (file, error) {
	raw, err := os.ReadFile(b.path)
	if errors.Is(err, os.ErrNotExist) {
		return file{}, nil
	}
	if err != nil {
		return file{}, err
	}
	var stored file
	if err := json.Unmarshal(raw, &stored); err != nil {
		return file{}, fmt.Errorf("定时任务文件损坏: %w", err)
	}
	return stored, nil
}

func (b *Book) write(stored file) error {
	if stored.Tasks == nil {
		stored.Tasks = []Task{}
	}
	raw, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, b.path); err == nil {
		return nil
	}
	_ = os.Remove(b.path)
	if err := os.Rename(tmp, b.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (b *Book) claim(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running[id] {
		return false
	}
	b.running[id] = true
	return true
}

func (b *Book) release(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.running, id)
}

func newID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

func trimResult(result string) string {
	result = strings.TrimSpace(result)
	result = strings.ReplaceAll(result, "\r\n", " ")
	result = strings.ReplaceAll(result, "\n", " ")
	if utf8.RuneCountInString(result) <= 160 {
		return result
	}
	return string([]rune(result)[:160]) + "…"
}
