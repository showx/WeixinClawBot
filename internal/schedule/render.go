package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/net/websocket"
)

const visibleTextJS = `(function () {
  var sels = [".el-main", "main", "#app", "body"];
  var best = "";
  for (var i = 0; i < sels.length; i++) {
    var node = document.querySelector(sels[i]);
    if (!node) continue;
    var text = (node.innerText || node.textContent || "").trim();
    if (text.length > best.length) best = text;
  }
  var pending = 0;
  var entries = performance.getEntriesByType("resource");
  for (var j = 0; j < entries.length; j++) {
    if (!entries[j].responseEnd) pending++;
  }
  var nav = performance.getEntriesByType("navigation")[0];
  if (nav && !nav.loadEventEnd) pending++;
  var loading = !!document.querySelector(".el-loading-mask, .el-loading-spinner, .el-skeleton, [aria-busy='true']");
  return { text: best, ready: document.readyState, pending: pending, loading: loading };
})()`

func renderWithBrowser(ctx context.Context, target pageTarget) (string, error) {
	bin := findBrowser()
	login, err := findLogin(target.URL)
	if err != nil {
		return "", err
	}
	if login.exe != "" {
		text, openErr := readOpenPage(ctx, login.exe, target.URL)
		if openErr != nil {
			return "", openErr
		}
		if strings.TrimSpace(text) == "" {
			return "", errors.New("页面没有读出文字")
		}
		return text, nil
	}
	if bin == "" {
		return "", errors.New("这个页面要运行脚本才有正文，但没有找到 Edge 或 Chrome")
	}
	dir, err := os.MkdirTemp("", "wxclaw-browser-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if login.userData != "" {
		if err := seedLogin(dir, login); err != nil {
			return "", err
		}
	}

	cmd := exec.Command(bin,
		"--headless=new",
		"--disable-gpu",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-sync",
		"--disable-extensions",
		"--mute-audio",
		"--remote-allow-origins=*",
		"--user-data-dir="+dir,
		"--profile-directory=Default",
		"--remote-debugging-port=0",
		"about:blank",
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	defer stopBrowser(cmd.Process)

	port, err := waitDevToolsPort(ctx, dir)
	if err != nil {
		return "", err
	}
	wsURL, err := waitPageSocket(ctx, port)
	if err != nil {
		return "", err
	}
	conn, err := dialBrowser(ctx, wsURL)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	session := newCDP(conn)
	defer session.Close()
	if _, err := session.call(ctx, "Page.enable", nil); err != nil {
		return "", err
	}
	if err := session.setCookies(ctx, target); err != nil {
		return "", err
	}
	if _, err := session.call(ctx, "Page.navigate", map[string]any{"url": target.URL}); err != nil {
		return "", err
	}
	text, err := session.waitForText(ctx)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("页面脚本跑完后仍然没有文字")
	}
	return text, nil
}

func findBrowser() string {
	var candidates []string
	if runtime.GOOS == "windows" {
		pf := os.Getenv("ProgramFiles")
		pf86 := os.Getenv("ProgramFiles(x86)")
		local := os.Getenv("LocalAppData")
		candidates = []string{
			filepath.Join(pf86, `Microsoft\Edge\Application\msedge.exe`),
			filepath.Join(pf, `Microsoft\Edge\Application\msedge.exe`),
			filepath.Join(pf, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(pf86, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(local, `Google\Chrome\Application\chrome.exe`),
		}
	} else {
		candidates = []string{"microsoft-edge", "google-chrome", "chromium", "chromium-browser"}
	}
	for _, path := range candidates {
		if path == "" {
			continue
		}
		if filepath.IsAbs(path) {
			info, err := os.Stat(path)
			if err == nil && !info.IsDir() {
				return path
			}
			continue
		}
		if found, err := exec.LookPath(path); err == nil {
			return found
		}
	}
	return ""
}

func waitDevToolsPort(ctx context.Context, dir string) (int, error) {
	path := filepath.Join(dir, "DevToolsActivePort")
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			line := strings.TrimSpace(strings.Split(string(raw), "\n")[0])
			port, convErr := strconv.Atoi(line)
			if convErr == nil && port > 0 {
				return port, nil
			}
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			return 0, errors.New("浏览器没有启动")
		}
		if !sleep(ctx, 100*time.Millisecond) {
			return 0, ctx.Err()
		}
	}
}

func waitPageSocket(ctx context.Context, port int) (string, error) {
	deadline := time.Now().Add(8 * time.Second)
	url := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			var pages []struct {
				Type string `json:"type"`
				WS   string `json:"webSocketDebuggerUrl"`
			}
			decodeErr := json.NewDecoder(resp.Body).Decode(&pages)
			resp.Body.Close()
			if decodeErr == nil {
				for _, page := range pages {
					if page.Type == "page" && page.WS != "" {
						return page.WS, nil
					}
				}
			}
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			return "", errors.New("浏览器没有打开页面")
		}
		if !sleep(ctx, 100*time.Millisecond) {
			return "", ctx.Err()
		}
	}
}

func dialBrowser(ctx context.Context, wsURL string) (*websocket.Conn, error) {
	type result struct {
		conn *websocket.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := websocket.Dial(wsURL, "", "http://127.0.0.1/")
		ch <- result{conn, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case item := <-ch:
		return item.conn, item.err
	}
}

type cdpResult struct {
	Result json.RawMessage
	Err    error
}

type cdpSession struct {
	ws     *websocket.Conn
	mu     sync.Mutex
	next   int
	wait   map[int]chan cdpResult
	closed chan struct{}
}

func newCDP(conn *websocket.Conn) *cdpSession {
	session := &cdpSession{
		ws:     conn,
		wait:   map[int]chan cdpResult{},
		closed: make(chan struct{}),
	}
	go session.readLoop()
	return session
}

func (s *cdpSession) Close() {
	s.mu.Lock()
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	s.mu.Unlock()
	_ = s.ws.Close()
}

func (s *cdpSession) readLoop() {
	for {
		var msg struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := websocket.JSON.Receive(s.ws, &msg); err != nil {
			s.failAll(err)
			return
		}
		if msg.ID == 0 {
			continue
		}
		result := cdpResult{Result: msg.Result}
		if msg.Error != nil && msg.Error.Message != "" {
			result.Err = errors.New(msg.Error.Message)
		}
		s.mu.Lock()
		ch := s.wait[msg.ID]
		delete(s.wait, msg.ID)
		s.mu.Unlock()
		if ch != nil {
			ch <- result
		}
	}
}

func (s *cdpSession) failAll(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ch := range s.wait {
		ch <- cdpResult{Err: err}
		delete(s.wait, id)
	}
}

func (s *cdpSession) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	s.next++
	id := s.next
	ch := make(chan cdpResult, 1)
	s.wait[id] = ch
	s.mu.Unlock()

	payload := map[string]any{"id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	if err := websocket.JSON.Send(s.ws, payload); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closed:
		return nil, errors.New("浏览器连接已断开")
	case result := <-ch:
		if result.Err != nil {
			return nil, result.Err
		}
		return result.Result, nil
	}
}

func (s *cdpSession) setCookies(ctx context.Context, target pageTarget) error {
	if strings.TrimSpace(target.Cookie) == "" {
		return nil
	}
	if _, err := s.call(ctx, "Network.enable", nil); err != nil {
		return err
	}
	for _, part := range strings.Split(target.Cookie, ";") {
		part = strings.TrimSpace(part)
		name, value, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		_, err := s.call(ctx, "Network.setCookie", map[string]any{
			"name":  strings.TrimSpace(name),
			"value": value,
			"url":   target.URL,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *cdpSession) waitForText(ctx context.Context) (string, error) {
	deadline := time.Now().Add(25 * time.Second)
	best := ""
	var quietSince time.Time
	for {
		view, err := s.pageView(ctx)
		if err == nil {
			text := strings.TrimSpace(view.Text)
			busy := view.Ready != "complete" || view.Pending > 0 || view.Loading
			if utf8.RuneCountInString(text) > utf8.RuneCountInString(best) {
				best = text
				quietSince = time.Time{}
			}
			if !busy && text == best && utf8.RuneCountInString(best) > 8 {
				if quietSince.IsZero() {
					quietSince = time.Now()
				}
				if time.Since(quietSince) >= 1200*time.Millisecond {
					break
				}
			} else if busy || text != best {
				quietSince = time.Time{}
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		if !sleep(ctx, 400*time.Millisecond) {
			break
		}
	}
	return strings.TrimSpace(best), nil
}

type pageView struct {
	Text    string `json:"text"`
	Ready   string `json:"ready"`
	Pending int    `json:"pending"`
	Loading bool   `json:"loading"`
}

func (s *cdpSession) pageView(ctx context.Context) (pageView, error) {
	raw, err := s.call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    visibleTextJS,
		"returnByValue": true,
	})
	if err != nil {
		return pageView{}, err
	}
	var payload struct {
		Result struct {
			Value pageView `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return pageView{}, err
	}
	return payload.Result.Value, nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func stopBrowser(proc *os.Process) {
	if proc == nil {
		return
	}
	if runtime.GOOS == "windows" {
		cmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(proc.Pid))
		hideWindow(cmd)
		_ = cmd.Run()
	}
	_ = proc.Kill()
}
