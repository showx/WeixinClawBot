// Package store 把登录凭证、长轮询游标和每个用户的 context_token 存在本地目录。
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Credentials struct {
	BotToken string    `json:"bot_token"`
	BaseURL  string    `json:"base_url"`
	BotID    string    `json:"bot_id"`
	UserID   string    `json:"user_id"`
	SavedAt  time.Time `json:"saved_at"`
}

const (
	// BlockExpired 表示微信已拒绝当前 context_token。
	BlockExpired = "expired"
	// BlockRateLimited 表示微信已限制继续主动发送。
	BlockRateLimited = "rate_limited"
)

type Session struct {
	UserID       string    `json:"user_id"`
	ContextToken string    `json:"context_token"`
	UpdatedAt    time.Time `json:"updated_at"`
	SentCount    int       `json:"sent_count"`
	LastSentAt   time.Time `json:"last_sent_at,omitempty"`
	BlockReason  string    `json:"block_reason,omitempty"`
	BlockedAt    time.Time `json:"blocked_at,omitempty"`
}

type State struct {
	GetUpdatesBuf string `json:"get_updates_buf"`
}

type fileSessions struct {
	Users         map[string]Session `json:"users"`
	DefaultUserID string             `json:"default_user_id"`
}

// Store 是单账号的本地状态。方法可并发调用。
type Store struct {
	dir string
	mu  sync.Mutex
}

func Open(dir string) (*Store, error) {
	if dir == "" {
		dir = "data"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) LoadCredentials() (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var creds Credentials
	err := readJSON(s.path("credentials.json"), &creds)
	if errors.Is(err, os.ErrNotExist) {
		return Credentials{}, nil
	}
	return creds, err
}

func (s *Store) SaveCredentials(creds Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if creds.SavedAt.IsZero() {
		creds.SavedAt = time.Now()
	}
	return writeJSON(s.path("credentials.json"), creds)
}

func (s *Store) ClearCredentials() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path("credentials.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) LoadCursor() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var state State
	err := readJSON(s.path("state.json"), &state)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return state.GetUpdatesBuf, err
}

func (s *Store) SaveCursor(cursor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSON(s.path("state.json"), State{GetUpdatesBuf: cursor})
}

func (s *Store) ClearCursor() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path("state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) UpsertSession(userID, contextToken string, at time.Time) error {
	if userID == "" || contextToken == "" {
		return errors.New("会话缺少用户或 context_token")
	}
	if at.IsZero() {
		at = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions, err := s.loadSessions()
	if err != nil {
		return err
	}
	sessions.Users[userID] = Session{UserID: userID, ContextToken: contextToken, UpdatedAt: at}
	sessions.DefaultUserID = userID
	return writeJSON(s.path("sessions.json"), sessions)
}

// NoteSend 累加自上次入站消息以来微信已接受的条数。
func (s *Store) NoteSend(userID string, n int) error {
	if n <= 0 {
		return nil
	}
	return s.updateSession(userID, func(item *Session) {
		item.SentCount += n
		item.LastSentAt = time.Now()
	})
}

// NoteBlocked 记下微信已经拒绝这个会话，直到对方再发来消息。
func (s *Store) NoteBlocked(userID, reason string) error {
	if reason != BlockExpired && reason != BlockRateLimited {
		return errors.New("未知的会话拒绝原因")
	}
	return s.updateSession(userID, func(item *Session) {
		item.BlockReason = reason
		item.BlockedAt = time.Now()
	})
}

func (s *Store) updateSession(userID string, update func(*Session)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions, err := s.loadSessions()
	if err != nil {
		return err
	}
	item, ok := sessions.Users[userID]
	if !ok {
		return errors.New("会话不存在")
	}
	update(&item)
	sessions.Users[userID] = item
	return writeJSON(s.path("sessions.json"), sessions)
}

func (s *Store) Session(userID string) (Session, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions, err := s.loadSessions()
	if err != nil {
		return Session{}, false, err
	}
	item, ok := sessions.Users[userID]
	return item, ok, nil
}

func (s *Store) LatestSession() (Session, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions, err := s.loadSessions()
	if err != nil {
		return Session{}, false, err
	}
	if sessions.DefaultUserID != "" {
		if item, ok := sessions.Users[sessions.DefaultUserID]; ok {
			return item, true, nil
		}
	}
	var latest Session
	found := false
	for _, item := range sessions.Users {
		if !found || item.UpdatedAt.After(latest.UpdatedAt) {
			latest = item
			found = true
		}
	}
	return latest, found, nil
}

func (s *Store) ListSessions() ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions, err := s.loadSessions()
	if err != nil {
		return nil, err
	}
	list := make([]Session, 0, len(sessions.Users))
	for _, item := range sessions.Users {
		list = append(list, item)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].UpdatedAt.After(list[j].UpdatedAt)
	})
	return list, nil
}

func (s *Store) ClearSessions() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path("sessions.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// APIToken 读取本地 HTTP 接口令牌；没有则生成并保存。
func (s *Store) APIToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path("api_token.txt")
	raw, err := os.ReadFile(path)
	if err == nil {
		token := string(bytesTrim(raw))
		if token != "" {
			return token, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	if err := writeFileAtomic(path, []byte(token+"\n")); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) loadSessions() (fileSessions, error) {
	sessions := fileSessions{Users: map[string]Session{}}
	err := readJSON(s.path("sessions.json"), &sessions)
	if errors.Is(err, os.ErrNotExist) {
		return sessions, nil
	}
	if err != nil {
		return fileSessions{}, err
	}
	if sessions.Users == nil {
		sessions.Users = map[string]Session{}
	}
	return sessions, nil
}

func (s *Store) path(name string) string {
	return filepath.Join(s.dir, name)
}

func readJSON(path string, dest any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dest)
}

func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writeFileAtomic(path, raw)
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
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

func bytesTrim(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}
