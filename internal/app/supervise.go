package app

import (
	"context"
	"sort"
	"sync"
	"time"

	"weixinclawbot/internal/store"
)

type watchSlot struct {
	cancel context.CancelFunc
	done   <-chan struct{}
	stop   bool
}

// Supervise 对齐正在监听的微信。新扫码或移除后，大约两秒内会跟上，不用重启进程。
func (a *App) Supervise(ctx context.Context) error {
	if a == nil || a.Catalog == nil {
		return ErrNotLoggedIn
	}
	slots := map[string]*watchSlot{}
	var coolMu sync.Mutex
	cool := map[string]time.Time{}
	refresh := func() {
		ready, err := a.Catalog.LoggedIn()
		if err != nil {
			a.logf("读取已登录微信失败: %v", err)
			return
		}
		want := map[string]store.Bound{}
		for _, item := range ready {
			want[item.Account.Name] = item
		}
		for name, slot := range slots {
			select {
			case <-slot.done:
				delete(slots, name)
			default:
			}
		}
		for name, slot := range slots {
			if _, ok := want[name]; ok || slot.stop {
				continue
			}
			slot.stop = true
			slot.cancel()
		}
		now := time.Now()
		for name, bound := range want {
			if _, ok := slots[name]; ok {
				continue
			}
			coolMu.Lock()
			until := cool[name]
			coolMu.Unlock()
			if now.Before(until) {
				continue
			}
			childCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			slots[name] = &watchSlot{cancel: cancel, done: done}
			bound := bound
			name := name
			go func() {
				defer close(done)
				err := a.child(bound).Watch(childCtx)
				if err != nil && childCtx.Err() == nil {
					a.logger().Printf("微信 %s 已停止监听: %v", bound.Account.Name, err)
					coolMu.Lock()
					cool[name] = time.Now().Add(15 * time.Second)
					coolMu.Unlock()
				}
			}()
		}
		names := make([]string, 0, len(slots))
		for name, slot := range slots {
			if !slot.stop {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		a.setWatching(names)
	}

	refresh()
	defer func() {
		for _, slot := range slots {
			slot.cancel()
		}
		for _, slot := range slots {
			<-slot.done
		}
		a.setWatching(nil)
	}()

	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			refresh()
		}
	}
}

// Watching 返回当前正在监听的微信名称。
func (a *App) Watching() []string {
	if a == nil {
		return nil
	}
	a.watchMu.Lock()
	defer a.watchMu.Unlock()
	out := make([]string, len(a.watching))
	copy(out, a.watching)
	return out
}

func (a *App) setWatching(names []string) {
	a.watchMu.Lock()
	a.watching = append([]string(nil), names...)
	a.watchMu.Unlock()
}
