package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"

	"weixinclawbot/internal/ilink"
	"weixinclawbot/internal/store"
)

var (
	errLoginBusy   = errors.New("已经有一个扫码登录在进行")
	errLoginNoCode = errors.New("现在不需要输入验证码")
)

// LoginView 是管理界面上的扫码进度，不包含登录凭证。
type LoginView struct {
	Phase   string `json:"phase"`
	Name    string `json:"name"`
	Message string `json:"message"`
	QR      string `json:"qr,omitempty"`
}

type deskLogin struct {
	app       *App
	mu        sync.Mutex
	running   bool
	dismissed bool
	cancel    context.CancelFunc
	phase     string
	name      string
	message   string
	qr        []byte
	codeCh    chan string
}

func (a *App) desktop() *deskLogin {
	a.deskOnce.Do(func() {
		a.desk = &deskLogin{app: a, phase: "idle"}
	})
	return a.desk
}

// LoginView 返回当前扫码进度。还没开始时是 idle。
func (a *App) LoginView() LoginView {
	if a == nil {
		return LoginView{Phase: "idle"}
	}
	return a.desktop().view()
}

// BeginLogin 在后台开始扫码。名称会显示在管理界面的微信列表里。
func (a *App) BeginLogin(name string) error {
	if err := store.ValidateAccountName(name); err != nil {
		return err
	}
	return a.desktop().start(strings.TrimSpace(name))
}

// SubmitLoginCode 提交手机微信上显示的数字。
func (a *App) SubmitLoginCode(code string) error {
	return a.desktop().submit(code)
}

// CancelLogin 取消进行中的扫码，或清掉已经结束的提示。
func (a *App) CancelLogin() {
	if a == nil {
		return
	}
	a.desktop().stop()
}

func (d *deskLogin) view() LoginView {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := LoginView{Phase: d.phase, Name: d.name, Message: d.message}
	if out.Phase == "" {
		out.Phase = "idle"
	}
	if len(d.qr) > 0 && (out.Phase == "qr" || out.Phase == "scanned" || out.Phase == "verify") {
		out.QR = "data:image/png;base64," + base64.StdEncoding.EncodeToString(d.qr)
	}
	return out
}

func (d *deskLogin) set(phase, message string, qr []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dismissed {
		return
	}
	d.phase = phase
	d.message = message
	if qr == nil {
		d.qr = nil
	} else {
		d.qr = append([]byte(nil), qr...)
	}
}

func (d *deskLogin) start(name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running {
		return errLoginBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.running = true
	d.dismissed = false
	d.cancel = cancel
	d.phase = "starting"
	d.name = name
	d.message = "正在获取二维码…"
	d.qr = nil
	d.codeCh = make(chan string, 1)
	go d.loop(ctx, name)
	return nil
}

func (d *deskLogin) submit(code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return errors.New("未输入验证码")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.running || d.phase != "verify" || d.codeCh == nil {
		return errLoginNoCode
	}
	select {
	case d.codeCh <- code:
		d.message = "正在核对数字…"
		return nil
	default:
		return errors.New("验证码已提交，请稍候")
	}
}

func (d *deskLogin) stop() {
	d.mu.Lock()
	cancel := d.cancel
	if d.running {
		d.dismissed = true
	}
	d.phase = "idle"
	d.message = ""
	d.qr = nil
	d.name = ""
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (d *deskLogin) loop(ctx context.Context, name string) {
	defer func() {
		d.mu.Lock()
		d.running = false
		d.cancel = nil
		if d.dismissed || (d.phase != "done" && d.phase != "error") {
			d.phase = "idle"
			d.message = ""
			d.qr = nil
			d.name = ""
		}
		d.dismissed = false
		d.mu.Unlock()
	}()

	localTokens, err := d.app.loginTokens()
	if err != nil {
		d.fail(ctx, err)
		return
	}
	refreshes := 0
	for {
		if ctx.Err() != nil {
			return
		}
		d.set("starting", "正在获取二维码…", nil)
		qr, err := d.app.client().FetchQR(ctx, localTokens)
		if err != nil {
			d.fail(ctx, err)
			return
		}
		png, err := qrcode.Encode(qr.ContentURL, qrcode.Medium, 360)
		if err != nil {
			d.fail(ctx, err)
			return
		}
		d.set("qr", "请用微信扫描二维码。", png)
		status, err := d.wait(ctx, qr.Code, png)
		if err != nil {
			d.fail(ctx, err)
			return
		}
		switch status.Status {
		case "confirmed":
			if status.BotToken == "" || status.BotID == "" {
				d.fail(ctx, errors.New("登录失败：微信没有返回 bot 凭证"))
				return
			}
			account, err := d.app.saveLogin(name, loginCreds(status))
			if err != nil {
				d.fail(ctx, err)
				return
			}
			d.set("done", fmt.Sprintf("已登录微信「%s」。请用这个微信给 ClawBot 发一条任意消息。", account.Name), nil)
			return
		case "binded_redirect":
			if len(localTokens) > 0 {
				d.set("done", "这个微信已经登录过。可以在列表里查看，或用另一个微信扫描新的二维码。", nil)
				return
			}
			d.fail(ctx, errors.New("微信提示已绑定，但本地没有这个账号"))
			return
		case "expired", "verify_code_blocked":
			refreshes++
			if refreshes > 3 {
				d.fail(ctx, errors.New("二维码多次失效，请稍后再试"))
				return
			}
			d.set("starting", "二维码已失效，正在刷新。", nil)
		default:
			d.fail(ctx, fmt.Errorf("登录未完成: %s", status.Status))
			return
		}
	}
}

func (d *deskLogin) fail(ctx context.Context, err error) {
	if ctx.Err() != nil || err == nil {
		return
	}
	d.set("error", err.Error(), nil)
}

func (d *deskLogin) wait(ctx context.Context, qrcodeValue string, png []byte) (ilink.QRStatus, error) {
	deadline := time.Now().Add(8 * time.Minute)
	pollBase := d.app.client().LoginBaseURL
	if pollBase == "" {
		pollBase = ilink.DefaultBaseURL
	}
	var verifyCode string
	scanned := false
	for {
		if err := ctx.Err(); err != nil {
			return ilink.QRStatus{}, err
		}
		if time.Now().After(deadline) {
			return ilink.QRStatus{}, errors.New("登录超时，请重新开始")
		}
		status, err := d.app.client().PollQR(ctx, pollBase, qrcodeValue, verifyCode)
		if err != nil {
			if ctx.Err() != nil {
				return ilink.QRStatus{}, ctx.Err()
			}
			return ilink.QRStatus{}, err
		}
		switch status.Status {
		case "wait":
			if !sleep(ctx, time.Second) {
				return ilink.QRStatus{}, ctx.Err()
			}
		case "scaned":
			verifyCode = ""
			if !scanned {
				d.set("scanned", "已扫码，请在手机上确认。", png)
				scanned = true
			}
			if !sleep(ctx, time.Second) {
				return ilink.QRStatus{}, ctx.Err()
			}
		case "need_verifycode":
			prompt := "请输入手机微信上显示的数字。"
			if verifyCode != "" {
				prompt = "数字不匹配，请重新输入。"
			}
			verifyCode = ""
			d.set("verify", prompt, png)
			code, err := d.readCode(ctx)
			if err != nil {
				return ilink.QRStatus{}, err
			}
			verifyCode = code
		case "scaned_but_redirect":
			if status.RedirectHost != "" {
				pollBase = hostToBase(status.RedirectHost)
				d.set("qr", "扫码节点切换到 "+pollBase, png)
			}
		case "confirmed", "expired", "binded_redirect", "verify_code_blocked":
			return status, nil
		default:
			if status.Status != "" {
				return status, nil
			}
		}
	}
}

func (d *deskLogin) readCode(ctx context.Context) (string, error) {
	d.mu.Lock()
	ch := d.codeCh
	d.mu.Unlock()
	if ch == nil {
		return "", errLoginNoCode
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case code := <-ch:
		return code, nil
	}
}
