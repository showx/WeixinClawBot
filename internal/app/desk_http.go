package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

func (s *Server) desk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.authorize(w, r) {
		return
	}
	body, err := s.deskBody()
	if err != nil {
		writeJSON(w, statusOf(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) deskBody() (map[string]any, error) {
	watching := []string{}
	login := LoginView{Phase: "idle"}
	accounts := []any{}
	if s.App != nil {
		watching = s.App.Watching()
		login = s.App.LoginView()
		grouped, err := s.App.AccountSessions("")
		if err != nil {
			return nil, err
		}
		online := map[string]bool{}
		if s.App.Catalog != nil {
			ready, err := s.App.Catalog.LoggedIn()
			if err != nil {
				return nil, err
			}
			for _, item := range ready {
				online[item.Account.Name] = true
			}
		}
		now := time.Now()
		for _, group := range grouped {
			sessions := make([]any, 0, len(group.Sessions))
			for _, item := range group.Sessions {
				sessions = append(sessions, sessionView(item, now))
			}
			name := group.Account.Name
			accounts = append(accounts, map[string]any{
				"name":     name,
				"user_id":  group.Account.UserID,
				"online":   online[name],
				"watching": containsName(watching, name),
				"sessions": sessions,
			})
		}
	}
	return map[string]any{
		"ok":       true,
		"listen":   s.Listen,
		"token":    s.Token,
		"watching": watching,
		"login":    login,
		"accounts": accounts,
	}, nil
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.beginLogin(w, r)
	case http.MethodDelete:
		if !s.authorize(w, r) {
			return
		}
		if s.App != nil {
			s.App.CancelLogin()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) beginLogin(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	if s.App == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "服务还没准备好"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求体不是合法 JSON"})
		return
	}
	if err := s.App.BeginLogin(req.Name); err != nil {
		writeJSON(w, loginHTTPStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "login": s.App.LoginView()})
}

func (s *Server) loginCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.authorize(w, r) {
		return
	}
	if s.App == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "服务还没准备好"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求体不是合法 JSON"})
		return
	}
	if err := s.App.SubmitLoginCode(req.Code); err != nil {
		writeJSON(w, loginHTTPStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) removeAccount(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	if s.App == nil || s.App.Catalog == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "没有可管理的微信"})
		return
	}
	if err := s.App.Catalog.Remove(r.URL.Query().Get("name")); err != nil {
		writeJSON(w, statusOf(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func loginHTTPStatus(err error) int {
	if errors.Is(err, errLoginBusy) || errors.Is(err, errLoginNoCode) {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

func containsName(list []string, name string) bool {
	for _, item := range list {
		if item == name {
			return true
		}
	}
	return false
}
