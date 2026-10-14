package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

const browseSnapshotJS = `(function () {
  var old = document.querySelectorAll("[data-wxclaw]");
  for (var k = 0; k < old.length; k++) old[k].removeAttribute("data-wxclaw");
  var sel = "a, button, input, textarea, select, [role='button'], [role='link']";
  var nodes = document.querySelectorAll(sel);
  var items = [];
  var n = 0;
  var seen = 0;
  for (var i = 0; i < nodes.length && n < 30 && seen < 250; i++) {
    seen++;
    var el = nodes[i];
    var type = (el.getAttribute("type") || "").toLowerCase();
    if (type === "hidden" || type === "password" || type === "file") continue;
    var rect = el.getBoundingClientRect();
    if (rect.width < 2 || rect.height < 2) continue;
    var style = window.getComputedStyle(el);
    if (!style || style.visibility === "hidden" || style.display === "none") continue;
    el.setAttribute("data-wxclaw", String(n));
    var text = el.innerText || el.getAttribute("aria-label") || el.getAttribute("placeholder") || el.value || el.getAttribute("title") || el.getAttribute("name") || "";
    text = String(text).replace(/\s+/g, " ").trim();
    if (text.length > 60) text = text.slice(0, 60);
    items.push({i: n, tag: el.tagName.toLowerCase(), text: text});
    n++;
  }
  var text = "";
  var roots = [".el-main", "main", "#app", "body"];
  for (var j = 0; j < roots.length; j++) {
    var node = document.querySelector(roots[j]);
    if (!node) continue;
    var t = (node.innerText || "").trim();
    if (t.length > text.length) text = t;
  }
  if (text.length > 8000) text = text.slice(0, 8000);
  return {text: text, elements: items};
})()`

type browseSession struct {
	dir     string
	cmd     *exec.Cmd
	session *cdpSession
}

func (b *browseSession) close() {
	if b == nil {
		return
	}
	if b.session != nil {
		b.session.Close()
	}
	if b.cmd != nil {
		stopBrowser(b.cmd.Process)
	}
	if b.dir != "" {
		_ = os.RemoveAll(b.dir)
	}
}

func openBrowseDriver(ctx context.Context, target browseTarget) (pageDriver, func(), error) {
	bin := findBrowser()
	if bin == "" {
		return nil, nil, errors.New("没有找到 Edge 或 Chrome")
	}
	login, err := findLogin(target.URL)
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "wxclaw-browse-")
	if err != nil {
		return nil, nil, err
	}
	sess := &browseSession{dir: dir}
	ok := false
	defer func() {
		if !ok {
			sess.close()
		}
	}()
	if login.userData != "" {
		if err := seedLogin(dir, login); err != nil {
			return nil, nil, err
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
		return nil, nil, errors.New("浏览器没有启动")
	}
	sess.cmd = cmd

	port, err := waitDevToolsPort(ctx, dir)
	if err != nil {
		return nil, nil, err
	}
	wsURL, err := waitPageSocket(ctx, port)
	if err != nil {
		return nil, nil, err
	}
	conn, err := dialBrowser(ctx, wsURL)
	if err != nil {
		return nil, nil, err
	}
	sess.session = newCDP(conn)
	if _, err := sess.session.call(ctx, "Page.enable", nil); err != nil {
		return nil, nil, err
	}
	page := pageTarget{URL: target.URL, Cookie: target.Cookie}
	if err := sess.session.setCookies(ctx, page); err != nil {
		return nil, nil, err
	}
	if _, err := sess.session.call(ctx, "Page.navigate", map[string]any{"url": target.URL}); err != nil {
		return nil, nil, err
	}
	_, _ = sess.session.waitForText(ctx)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	ok = true
	return &cdpDriver{session: sess.session}, sess.close, nil
}

type cdpDriver struct {
	session *cdpSession
}

func (d *cdpDriver) Snapshot(ctx context.Context) (pageShot, error) {
	var shot pageShot
	if err := d.session.evalInto(ctx, browseSnapshotJS, &shot); err != nil {
		return pageShot{}, err
	}
	if shot.Elements == nil {
		shot.Elements = []pageControl{}
	}
	return shot, nil
}

func (d *cdpDriver) Click(ctx context.Context, index int) error {
	expr := fmt.Sprintf(`(function () {
	  var el = document.querySelector('[data-wxclaw="%d"]');
	  if (!el) return "missing";
	  el.click();
	  return "ok";
	})()`, index)
	status, err := d.evalStatus(ctx, expr)
	if err != nil {
		return err
	}
	if status != "ok" {
		return errElementMissing
	}
	return d.settle(ctx)
}

func (d *cdpDriver) Type(ctx context.Context, index int, text string) error {
	encoded, err := json.Marshal(text)
	if err != nil {
		return err
	}
	expr := fmt.Sprintf(`(function () {
	  var el = document.querySelector('[data-wxclaw="%d"]');
	  if (!el) return "missing";
	  var tag = (el.tagName || "").toLowerCase();
	  var type = (el.getAttribute("type") || "").toLowerCase();
	  if (type === "password" || type === "hidden" || type === "file" || type === "checkbox" || type === "radio" || type === "button" || type === "submit") return "notype";
	  if (tag !== "input" && tag !== "textarea" && tag !== "select") return "notype";
	  el.focus();
	  var proto = Object.getPrototypeOf(el);
	  var desc = Object.getOwnPropertyDescriptor(proto, "value");
	  var value = %s;
	  if (desc && desc.set) { desc.set.call(el, value); } else { el.value = value; }
	  el.dispatchEvent(new Event("input", {bubbles: true}));
	  el.dispatchEvent(new Event("change", {bubbles: true}));
	  return "ok";
	})()`, index, string(encoded))
	status, err := d.evalStatus(ctx, expr)
	if err != nil {
		return err
	}
	if status == "missing" {
		return errElementMissing
	}
	if status != "ok" {
		return errCannotType
	}
	return d.settle(ctx)
}

func (d *cdpDriver) Scroll(ctx context.Context, direction string) error {
	sign := ""
	if direction == "up" {
		sign = "-"
	}
	expr := fmt.Sprintf(`(function () {
	  var h = Math.round((window.innerHeight || 600) * 0.8);
	  window.scrollBy(0, %sh);
	  return "ok";
	})()`, sign)
	if _, err := d.evalStatus(ctx, expr); err != nil {
		return err
	}
	return d.settle(ctx)
}

func (d *cdpDriver) evalStatus(ctx context.Context, expression string) (string, error) {
	var status string
	if err := d.session.evalInto(ctx, expression, &status); err != nil {
		return "", err
	}
	return status, nil
}

func (d *cdpDriver) settle(ctx context.Context) error {
	deadline := time.Now().Add(8 * time.Second)
	var quiet time.Time
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		view, err := d.session.pageView(ctx)
		if err == nil {
			busy := view.Ready != "complete" || view.Pending > 0 || view.Loading
			if !busy {
				if quiet.IsZero() {
					quiet = time.Now()
				}
				if time.Since(quiet) >= 800*time.Millisecond {
					return nil
				}
			} else {
				quiet = time.Time{}
			}
		}
		if time.Now().After(deadline) {
			return nil
		}
		if !sleep(ctx, 300*time.Millisecond) {
			return ctx.Err()
		}
	}
}

func (s *cdpSession) evalInto(ctx context.Context, expression string, dest any) error {
	raw, err := s.call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
	})
	if err != nil {
		return err
	}
	var payload struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return errors.New("浏览器没有返回页面内容")
	}
	if len(bytesTrimSpace(payload.Exception)) > 0 && string(bytesTrimSpace(payload.Exception)) != "null" {
		return errors.New("页面脚本没有跑完")
	}
	if len(bytesTrimSpace(payload.Result.Value)) == 0 || string(bytesTrimSpace(payload.Result.Value)) == "null" {
		return errors.New("页面没有读出内容")
	}
	if err := json.Unmarshal(payload.Result.Value, dest); err != nil {
		return errors.New("页面没有读出内容")
	}
	return nil
}
