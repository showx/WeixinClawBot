package app

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"weixinclawbot/internal/store"
)

type Server struct {
	App    *App
	Token  string
	Listen string
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		s.send(w, r)
	})
	mux.HandleFunc("/v1/accounts", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.accounts(w, r)
		case http.MethodDelete:
			s.removeAccount(w, r)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		s.sessions(w, r)
	})
	mux.HandleFunc("/v1/desk", s.desk)
	mux.HandleFunc("/v1/login", s.login)
	mux.HandleFunc("/v1/login/code", s.loginCode)
	return mux
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var req struct {
		Account string `json:"account"`
		To      string `json:"to"`
		Text    string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求体不是合法 JSON"})
		return
	}
	result, err := s.App.SendAccount(r.Context(), req.Account, req.To, req.Text)
	if err != nil {
		writeJSON(w, statusOf(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": result.Account, "to": result.To, "parts": result.Parts})
}

func (s *Server) accounts(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	if s.App.Catalog == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accounts": []any{}})
		return
	}
	list, err := s.App.Catalog.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accounts": accountViews(list)})
}

func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	grouped, err := s.App.AccountSessions(r.URL.Query().Get("account"))
	if err != nil {
		writeJSON(w, statusOf(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	now := time.Now()
	out := make([]map[string]any, 0, len(grouped))
	for _, group := range grouped {
		sessions := make([]map[string]any, 0, len(group.Sessions))
		for _, item := range group.Sessions {
			sessions = append(sessions, sessionView(item, now))
		}
		out = append(out, map[string]any{
			"account":  group.Account.Name,
			"user_id":  group.Account.UserID,
			"sessions": sessions,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accounts": out})
}

func accountViews(list []store.Account) []map[string]string {
	out := make([]map[string]string, 0, len(list))
	for _, item := range list {
		out = append(out, map[string]string{"name": item.Name, "user_id": item.UserID})
	}
	return out
}

func sessionView(item store.Session, now time.Time) map[string]any {
	view := DescribeSession(item, now)
	return map[string]any{
		"user_id":            view.UserID,
		"updated_at":         view.UpdatedAt.Format(time.RFC3339),
		"sent_since_inbound": view.SentCount,
		"status":             view.Status,
		"detail":             view.Detail,
	}
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	if tokenMatches(s.Token, bearerToken(r)) || tokenMatches(s.Token, cookieToken(r)) {
		return true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "本地接口令牌不正确"})
	return false
}

func bearerToken(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

func cookieToken(r *http.Request) string {
	cookie, err := r.Cookie("weixin_gui")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func tokenMatches(want, got string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func statusOf(err error) int {
	switch {
	case errors.Is(err, ErrNotLoggedIn):
		return http.StatusUnauthorized
	case errors.Is(err, ErrNoSession), errors.Is(err, ErrTokenRejected), errors.Is(err, ErrRateLimited):
		return http.StatusConflict
	case errors.Is(err, store.ErrAccountNotFound):
		return http.StatusNotFound
	case errors.Is(err, store.ErrAccountRequired), errors.Is(err, store.ErrNameTaken):
		return http.StatusBadRequest
	case strings.Contains(err.Error(), "消息内容不能为空"), strings.Contains(err.Error(), "文本过长"):
		return http.StatusBadRequest
	default:
		return http.StatusBadGateway
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
