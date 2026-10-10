package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"weixinclawbot/internal/ilink"
	"weixinclawbot/internal/store"
)

// AccountSessions 是一个微信下面当前可以发送的会话。
type AccountSessions struct {
	Account  store.Account
	Sessions []store.Session
}

func (a *App) dataDir() string {
	if a.Store != nil {
		return a.Store.Dir()
	}
	if a.Catalog != nil {
		return a.Catalog.Dir()
	}
	return "data"
}

func (a *App) logf(format string, args ...any) {
	if a.Name != "" {
		a.logger().Printf("微信 %s："+format, append([]any{a.Name}, args...)...)
		return
	}
	a.logger().Printf(format, args...)
}

// WatchAccounts 同时监听一个或全部已登录微信。name 为空时监听全部。
func (a *App) WatchAccounts(ctx context.Context, name string) error {
	bounds, err := a.ReadyAccounts(name)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	for _, bound := range bounds {
		bound := bound
		wg.Add(1)
		go func() {
			defer wg.Done()
			child := a.child(bound)
			if err := child.Watch(ctx); err != nil && ctx.Err() == nil {
				a.logger().Printf("微信 %s 已停止监听: %v", bound.Account.Name, err)
			}
		}()
	}
	wg.Wait()
	return nil
}

// SendAccount 用指定微信发消息。多个微信时必须给出名称。
func (a *App) SendAccount(ctx context.Context, accountName, to, text string) (SendResult, error) {
	if a.Catalog == nil {
		return a.Send(ctx, to, text)
	}
	bound, err := a.Catalog.Open(accountName)
	if err != nil {
		return SendResult{}, accountErr(err)
	}
	result, err := a.child(bound).Send(ctx, to, text)
	result.Account = bound.Account.Name
	return result, err
}

// AccountSessions 列出一个或全部微信的会话。
func (a *App) AccountSessions(name string) ([]AccountSessions, error) {
	if a.Catalog == nil {
		list, err := a.Sessions()
		if err != nil {
			return nil, err
		}
		return []AccountSessions{{Sessions: list}}, nil
	}
	var bounds []store.Bound
	if strings.TrimSpace(name) == "" {
		list, err := a.Catalog.List()
		if err != nil {
			return nil, err
		}
		for _, item := range list {
			bound, err := a.Catalog.Open(item.Name)
			if err != nil {
				return nil, err
			}
			bounds = append(bounds, bound)
		}
	} else {
		bound, err := a.Catalog.Open(name)
		if err != nil {
			return nil, accountErr(err)
		}
		bounds = append(bounds, bound)
	}
	out := make([]AccountSessions, 0, len(bounds))
	for _, bound := range bounds {
		list, err := bound.Store.ListSessions()
		if err != nil {
			return nil, err
		}
		out = append(out, AccountSessions{Account: bound.Account, Sessions: list})
	}
	return out, nil
}

func (a *App) ReadyAccounts(name string) ([]store.Bound, error) {
	if a.Catalog == nil {
		return nil, ErrNotLoggedIn
	}
	if strings.TrimSpace(name) != "" {
		bound, err := a.Catalog.Open(name)
		if err != nil {
			return nil, accountErr(err)
		}
		creds, err := bound.Store.LoadCredentials()
		if err != nil {
			return nil, err
		}
		if creds.BotToken == "" || creds.BaseURL == "" {
			return nil, fmt.Errorf("微信 %s 的登录已失效，请重新执行 login -name %s", bound.Account.Name, bound.Account.Name)
		}
		return []store.Bound{bound}, nil
	}
	ready, err := a.Catalog.LoggedIn()
	if err != nil {
		return nil, err
	}
	if len(ready) == 0 {
		return nil, ErrNotLoggedIn
	}
	return ready, nil
}

func (a *App) child(bound store.Bound) *App {
	return &App{Store: bound.Store, Client: a.client(), Log: a.Log, Name: bound.Account.Name}
}

func accountErr(err error) error {
	if errors.Is(err, store.ErrNoAccounts) {
		return ErrNotLoggedIn
	}
	return err
}

func (a *App) saveLogin(name string, creds store.Credentials) (store.Account, error) {
	if a.Catalog != nil {
		account, err := a.Catalog.Upsert(name, creds)
		if err != nil {
			return store.Account{}, err
		}
		bound, err := a.Catalog.Open(account.Name)
		if err != nil {
			return store.Account{}, err
		}
		if err := bound.Store.ClearCursor(); err != nil {
			return store.Account{}, err
		}
		return account, nil
	}
	if err := a.Store.SaveCredentials(creds); err != nil {
		return store.Account{}, err
	}
	if err := a.Store.ClearCursor(); err != nil {
		return store.Account{}, err
	}
	return store.Account{Name: name, BotID: creds.BotID, UserID: creds.UserID, BaseURL: creds.BaseURL}, nil
}

func (a *App) loginTokens() ([]string, error) {
	if a.Catalog != nil {
		return a.Catalog.Tokens()
	}
	if a.Store == nil {
		return nil, nil
	}
	creds, err := a.Store.LoadCredentials()
	if err != nil || creds.BotToken == "" {
		return nil, err
	}
	return []string{creds.BotToken}, nil
}

func loginCreds(status ilink.QRStatus) store.Credentials {
	baseURL := strings.TrimSpace(status.BaseURL)
	if baseURL == "" {
		baseURL = ilink.DefaultBaseURL
	}
	return store.Credentials{
		BotToken: status.BotToken,
		BaseURL:  strings.TrimRight(baseURL, "/"),
		BotID:    status.BotID,
		UserID:   status.UserID,
	}
}
