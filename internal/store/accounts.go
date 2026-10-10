package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrNoAccounts      = errors.New("尚未登录微信，请先执行 login")
	ErrAccountRequired = errors.New("已登录多个微信，请用 -account 指定名称，可执行 accounts 查看")
	ErrAccountNotFound = errors.New("没有这个微信，可执行 accounts 查看")
	ErrNameTaken       = errors.New("这个名称已经用于另一个微信")
)

// Account 是一个已扫码的微信。名称由本地指定，BotID 是微信返回的账号 ID。
type Account struct {
	Name    string `json:"name"`
	BotID   string `json:"bot_id"`
	UserID  string `json:"user_id"`
	BaseURL string `json:"base_url"`
}

// Bound 是账号目录和它的状态存储。
type Bound struct {
	Account Account
	Store   *Store
}

type Catalog struct {
	root *Store
}

type catalogFile struct {
	Accounts []Account `json:"accounts"`
}

func OpenCatalog(dir string) (*Catalog, error) {
	root, err := Open(dir)
	if err != nil {
		return nil, err
	}
	catalog := &Catalog{root: root}
	if err := catalog.importLegacy(); err != nil {
		return nil, err
	}
	return catalog, nil
}

func (c *Catalog) Dir() string { return c.root.Dir() }

func (c *Catalog) APIToken() (string, error) { return c.root.APIToken() }

func (c *Catalog) List() ([]Account, error) {
	file, err := c.load()
	if err != nil {
		return nil, err
	}
	return file.Accounts, nil
}

// Tokens 返回已保存的 bot token，供再次扫码时识别已经绑定的微信。
func (c *Catalog) Tokens() ([]string, error) {
	list, err := c.List()
	if err != nil {
		return nil, err
	}
	var tokens []string
	for i := len(list) - 1; i >= 0 && len(tokens) < 10; i-- {
		bound, err := c.open(list[i])
		if err != nil {
			return nil, err
		}
		creds, err := bound.Store.LoadCredentials()
		if err != nil {
			return nil, err
		}
		if creds.BotToken != "" {
			tokens = append(tokens, creds.BotToken)
		}
	}
	return tokens, nil
}

// Upsert 按微信账号保存登录。同一次扫码再次登录会更新原账号；空名称会自动编号。
func (c *Catalog) Upsert(preferredName string, creds Credentials) (Account, error) {
	if creds.BotToken == "" || creds.BotID == "" {
		return Account{}, errors.New("登录凭证不完整")
	}
	preferredName = strings.TrimSpace(preferredName)
	if preferredName != "" {
		if err := validAccountName(preferredName); err != nil {
			return Account{}, err
		}
	}
	file, err := c.load()
	if err != nil {
		return Account{}, err
	}
	index := -1
	for i, item := range file.Accounts {
		if item.BotID == creds.BotID {
			index = i
		}
		if preferredName != "" && item.Name == preferredName && item.BotID != creds.BotID {
			return Account{}, ErrNameTaken
		}
	}
	account := Account{Name: preferredName, BotID: creds.BotID, UserID: creds.UserID, BaseURL: creds.BaseURL}
	if index >= 0 {
		if account.Name == "" {
			account.Name = file.Accounts[index].Name
		}
		file.Accounts[index] = account
	} else {
		if account.Name == "" {
			account.Name = nextAccountName(file.Accounts)
		}
		file.Accounts = append(file.Accounts, account)
	}
	bound, err := c.open(account)
	if err != nil {
		return Account{}, err
	}
	if err := bound.Store.SaveCredentials(creds); err != nil {
		return Account{}, err
	}
	if err := c.save(file); err != nil {
		return Account{}, err
	}
	return account, nil
}

// Open 按名称打开一个微信。名称为空时，只有恰好一个微信才能打开。
func (c *Catalog) Open(name string) (Bound, error) {
	name = strings.TrimSpace(name)
	list, err := c.List()
	if err != nil {
		return Bound{}, err
	}
	if name == "" {
		switch len(list) {
		case 0:
			return Bound{}, ErrNoAccounts
		case 1:
			name = list[0].Name
		default:
			return Bound{}, ErrAccountRequired
		}
	}
	for _, item := range list {
		if item.Name == name {
			return c.open(item)
		}
	}
	return Bound{}, ErrAccountNotFound
}

// LoggedIn 返回仍有登录凭证的微信。
func (c *Catalog) LoggedIn() ([]Bound, error) {
	list, err := c.List()
	if err != nil {
		return nil, err
	}
	var ready []Bound
	for _, item := range list {
		bound, err := c.open(item)
		if err != nil {
			return nil, err
		}
		creds, err := bound.Store.LoadCredentials()
		if err != nil {
			return nil, err
		}
		if creds.BotToken != "" && creds.BaseURL != "" {
			ready = append(ready, bound)
		}
	}
	return ready, nil
}

func (c *Catalog) importLegacy() error {
	creds, err := c.root.LoadCredentials()
	if err != nil || creds.BotToken == "" {
		return err
	}
	if creds.BotID == "" {
		creds.BotID = "legacy"
	}
	file, err := c.load()
	if err != nil {
		return err
	}
	for _, item := range file.Accounts {
		if item.BotID == creds.BotID {
			return c.removeLegacy()
		}
	}
	account, err := c.Upsert("", creds)
	if err != nil {
		return err
	}
	bound, err := c.Open(account.Name)
	if err != nil {
		return err
	}
	if err := moveIfExists(c.root.path("sessions.json"), bound.Store.path("sessions.json")); err != nil {
		return err
	}
	if err := moveIfExists(c.root.path("state.json"), bound.Store.path("state.json")); err != nil {
		return err
	}
	return c.removeLegacy()
}

func (c *Catalog) removeLegacy() error {
	for _, name := range []string{"credentials.json", "sessions.json", "state.json"} {
		err := os.Remove(c.root.path(name))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (c *Catalog) open(account Account) (Bound, error) {
	st, err := Open(filepath.Join(c.root.Dir(), "accounts", sanitize(account.BotID)))
	if err != nil {
		return Bound{}, err
	}
	return Bound{Account: account, Store: st}, nil
}

func (c *Catalog) load() (catalogFile, error) {
	var file catalogFile
	err := readJSON(c.root.path("accounts.json"), &file)
	if errors.Is(err, os.ErrNotExist) {
		return catalogFile{}, nil
	}
	return file, err
}

func (c *Catalog) save(file catalogFile) error {
	return writeJSON(c.root.path("accounts.json"), file)
}

func nextAccountName(list []Account) string {
	used := map[string]bool{}
	for _, item := range list {
		used[item.Name] = true
	}
	for i := 1; ; i++ {
		name := fmt.Sprintf("微信%d", i)
		if !used[name] {
			return name
		}
	}
}

func validAccountName(name string) error {
	if strings.ContainsAny(name, `/\`) {
		return errors.New("微信名称不能包含斜杠")
	}
	if len([]rune(name)) > 32 {
		return errors.New("微信名称不能超过 32 个字")
	}
	return nil
}

func sanitize(botID string) string {
	var b strings.Builder
	for _, r := range botID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "account"
	}
	return b.String()
}

func moveIfExists(src, dst string) error {
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	_ = os.Remove(dst)
	return os.Rename(src, dst)
}
