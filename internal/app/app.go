package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"weixinclawbot/internal/ilink"
	"weixinclawbot/internal/store"
)

var (
	ErrNotLoggedIn   = errors.New("尚未登录，请先执行 login")
	ErrNoSession     = errors.New("还没有可发送的会话：请让对方先给 ClawBot 发一条消息，并保持 serve 运行")
	ErrTokenRejected = errors.New("微信已拒绝当前会话令牌，请让对方再给 ClawBot 发一条消息")
	ErrRateLimited   = errors.New("微信已限制继续发送，请让对方再给 ClawBot 发一条消息")
)

type SendResult struct {
	Account   string
	To        string
	Parts     int
	SentSince int
	Trace     string
}

// App 负责扫码登录、缓存 context_token，以及主动发文本。
// Catalog 存在时可以管理多个微信；Store 是当前正在操作的那一个微信。
type App struct {
	Catalog *store.Catalog
	Store   *store.Store
	Name    string
	Client  *ilink.Client
	Log     *log.Logger

	deskOnce sync.Once
	desk     *deskLogin
	watchMu  sync.Mutex
	watching []string

	incomingOnce sync.Once
	incoming     *incomingBox
}

// Incoming 是对方从手机发给 ClawBot 的一条消息。
type Incoming struct {
	Account string
	UserID  string
	Text    string
}

type incomingBox struct {
	mu sync.Mutex
	fn func(Incoming)
}

// SetOnIncoming 在收到手机发来的消息时回调。监听开始前设置即可，之后新登录的微信也会用同一份。
func (a *App) SetOnIncoming(fn func(Incoming)) {
	if a == nil {
		return
	}
	box := a.incomingHook()
	box.mu.Lock()
	box.fn = fn
	box.mu.Unlock()
}

func (a *App) incomingHook() *incomingBox {
	a.incomingOnce.Do(func() {
		a.incoming = &incomingBox{}
	})
	return a.incoming
}

func (a *App) emitIncoming(userID, text string) {
	text = strings.TrimSpace(text)
	if a == nil || a.incoming == nil || text == "" {
		return
	}
	a.incoming.mu.Lock()
	fn := a.incoming.fn
	a.incoming.mu.Unlock()
	if fn == nil {
		return
	}
	fn(Incoming{Account: a.Name, UserID: userID, Text: text})
}

func (a *App) logger() *log.Logger {
	if a.Log != nil {
		return a.Log
	}
	return log.Default()
}

func (a *App) client() *ilink.Client {
	if a.Client != nil {
		return a.Client
	}
	return ilink.NewClient()
}

// Login 扫码绑定一个微信。name 是本地名称；为空时自动编号。再次扫同一个微信会更新原账号。
func (a *App) Login(ctx context.Context, name string, in io.Reader, out io.Writer) error {
	localTokens, err := a.loginTokens()
	if err != nil {
		return err
	}
	reader := bufio.NewReader(in)
	refreshes := 0
	for {
		qr, err := a.client().FetchQR(ctx, localTokens)
		if err != nil {
			return err
		}
		if err := a.showQR(out, qr.ContentURL); err != nil {
			return err
		}
		status, err := a.waitQR(ctx, reader, out, qr.Code)
		if err != nil {
			return err
		}
		switch status.Status {
		case "confirmed":
			if status.BotToken == "" || status.BotID == "" {
				return errors.New("登录失败：微信没有返回 bot 凭证")
			}
			account, err := a.saveLogin(name, loginCreds(status))
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "已登录微信「%s」。请用这个微信给 ClawBot 发一条任意消息，然后执行 serve。\n", account.Name)
			return nil
		case "binded_redirect":
			if len(localTokens) > 0 {
				fmt.Fprintln(out, "这个微信已经登录过。可用 accounts 查看，或用另一个微信扫描新的二维码。")
				return nil
			}
			return errors.New("微信提示已绑定，但本地没有这个账号")
		case "expired", "verify_code_blocked":
			refreshes++
			if refreshes > 3 {
				return errors.New("二维码多次失效，请稍后再执行 login")
			}
			fmt.Fprintln(out, "二维码已失效，正在刷新。")
		default:
			return fmt.Errorf("登录未完成: %s", status.Status)
		}
	}
}

func (a *App) showQR(out io.Writer, contentURL string) error {
	path, err := writeQRFile(a.dataDir(), contentURL)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "请用微信扫描二维码。")
	if enableTerminalQR() {
		if err := writeTerminalQR(out, contentURL); err != nil {
			fmt.Fprintln(out, err.Error())
		}
	}
	if err := openImage(path); err != nil {
		fmt.Fprintf(out, "图片没有自动打开：%v\n请手动打开这个文件：%s\n", err, path)
		return nil
	}
	fmt.Fprintf(out, "已用画图打开：%s\n", path)
	return nil
}

func (a *App) waitQR(ctx context.Context, in *bufio.Reader, out io.Writer, qrcode string) (ilink.QRStatus, error) {
	deadline := time.Now().Add(8 * time.Minute)
	pollBase := a.client().LoginBaseURL
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
			return ilink.QRStatus{}, errors.New("登录超时，请重新执行 login")
		}
		status, err := a.client().PollQR(ctx, pollBase, qrcode, verifyCode)
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
				fmt.Fprintln(out, "已扫码，请在手机上确认。")
				scanned = true
			}
			if !sleep(ctx, time.Second) {
				return ilink.QRStatus{}, ctx.Err()
			}
		case "need_verifycode":
			prompt := "输入手机微信上显示的数字："
			if verifyCode != "" {
				prompt = "数字不匹配，请重新输入："
			}
			fmt.Fprint(out, prompt)
			line, err := in.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return ilink.QRStatus{}, err
			}
			verifyCode = strings.TrimSpace(line)
			if verifyCode == "" {
				return ilink.QRStatus{}, errors.New("未输入验证码")
			}
		case "scaned_but_redirect":
			if status.RedirectHost != "" {
				pollBase = hostToBase(status.RedirectHost)
				fmt.Fprintf(out, "扫码节点切换到 %s\n", pollBase)
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

func hostToBase(host string) string {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "https://") || strings.HasPrefix(host, "http://") {
		return strings.TrimRight(host, "/")
	}
	return "https://" + host
}

// Watch 长轮询入站消息，只为刷新每个用户的 context_token，不会自动回复。
func (a *App) Watch(ctx context.Context) error {
	creds, err := a.loggedIn()
	if err != nil {
		return err
	}
	if err := a.client().NotifyStart(ctx, toIlink(creds)); err != nil {
		return err
	}
	defer func() {
		if err := a.client().NotifyStop(context.Background(), toIlink(creds)); err != nil {
			a.logf("停止通知失败: %v", err)
		}
	}()

	cursor, err := a.Store.LoadCursor()
	if err != nil {
		return err
	}
	hold := 35 * time.Second
	failures := 0
	a.logf("开始监听微信消息，用来刷新主动发送所需的会话")
	for {
		if ctx.Err() != nil {
			return nil
		}
		updates, err := a.client().GetUpdates(ctx, toIlink(creds), cursor, hold+5*time.Second)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			if ilink.IsSessionExpired(err) {
				_ = a.Store.ClearCredentials()
				_ = a.Store.ClearCursor()
				_ = a.Store.ClearSessions()
				return err
			}
			failures++
			wait := 2 * time.Second
			if failures >= 3 {
				wait = 30 * time.Second
			}
			a.logf("监听失败，%s 后重试: %v", wait, err)
			if !sleep(ctx, wait) {
				return nil
			}
			continue
		}
		failures = 0
		if updates.PollTimeout > 0 {
			hold = updates.PollTimeout
		}
		for _, msg := range updates.Messages {
			userID := ilink.PeerUserID(msg, creds.BotID)
			text := strings.TrimSpace(msg.Text)
			if msg.ContextToken == "" || userID == "" {
				if text != "" {
					a.emitIncoming(userID, text)
				}
				continue
			}
			prev, ok, err := a.Store.Session(userID)
			if err != nil {
				return err
			}
			if err := a.Store.UpsertSession(userID, msg.ContextToken, time.Now()); err != nil {
				return err
			}
			if text != "" {
				if noteErr := a.Store.NoteInbound(userID, text); noteErr != nil {
					a.logf("记下手机消息失败: %v", noteErr)
				}
				a.emitIncoming(userID, text)
			}
			if ok && prev.BlockReason != "" {
				a.logf("对方发来新消息，已重新开放发送: %s", userID)
				continue
			}
			if text != "" {
				a.logf("收到手机消息 %s：%s", userID, text)
				continue
			}
			a.logf("已刷新可发送会话: %s", userID)
		}
		if updates.Cursor != cursor {
			cursor = updates.Cursor
			if err := a.Store.SaveCursor(cursor); err != nil {
				return err
			}
		}
	}
}

// Send 主动发送一条文本。to 为空时发给最近发过消息的用户。
func (a *App) Send(ctx context.Context, to, text string) (SendResult, error) {
	creds, err := a.loggedIn()
	if err != nil {
		return SendResult{}, err
	}
	session, err := a.pickSession(to)
	if err != nil {
		return SendResult{}, err
	}
	steps := []string{sessionTrace(session, time.Now())}
	sent, err := a.client().SendText(ctx, toIlink(creds), session.UserID, session.ContextToken, text)
	steps = append(steps, "已向微信提交发送。")
	if sent > 0 {
		if noteErr := a.Store.NoteSend(session.UserID, sent); noteErr != nil {
			if err != nil {
				return SendResult{To: session.UserID, Parts: sent}, traceErr(err, append(steps, "记录发送次数失败："+noteErr.Error()))
			}
			return SendResult{}, noteErr
		}
	}
	if err != nil {
		steps = append(steps, wechatTrace(err))
		if kind, ok := ilink.RejectKind(err); ok {
			reason := store.BlockExpired
			userErr := ErrTokenRejected
			next := "微信拒绝了当前会话令牌。下次仍会用这个令牌再试；如果微信继续拒绝，需要对方再给 ClawBot 发一条消息。窗口或 serve 开着时，这条新消息会换成新令牌。"
			if kind == ilink.RejectRateLimited {
				reason = store.BlockRateLimited
				userErr = ErrRateLimited
				next = "微信限制了继续发送。下次仍会再试；如果继续被拒，需要对方再给 ClawBot 发一条消息。"
			}
			if noteErr := a.Store.NoteBlocked(session.UserID, reason); noteErr != nil {
				return SendResult{To: session.UserID, Parts: sent}, traceErr(userErr, append(steps, "记下拒绝失败："+noteErr.Error()))
			}
			return SendResult{To: session.UserID, Parts: sent}, traceErr(userErr, append(steps, next))
		}
		return SendResult{To: session.UserID, Parts: sent}, traceErr(err, steps)
	}
	updated, _, _ := a.Store.Session(session.UserID)
	steps = append(steps, fmt.Sprintf("微信已接受，本次 %d 条，此后累计 %d 条。", sent, updated.SentCount))
	return SendResult{To: session.UserID, Parts: sent, SentSince: updated.SentCount, Trace: strings.Join(steps, "\n")}, nil
}

func sessionTrace(session store.Session, now time.Time) string {
	when := "某次"
	if !session.UpdatedAt.IsZero() {
		when = ago(now.Sub(session.UpdatedAt)) + "（" + session.UpdatedAt.Format("01-02 15:04") + "）"
	}
	line := fmt.Sprintf("选用会话 %s。令牌来自对方%s发给 ClawBot 的消息，此后微信已接受 %d 条。", session.UserID, when, session.SentCount)
	if session.BlockReason == "" || session.BlockedAt.IsZero() {
		return line
	}
	why := "拒绝了会话令牌"
	if session.BlockReason == store.BlockRateLimited {
		why = "限制了继续发送"
	}
	return line + fmt.Sprintf(" 上次 %s 微信%s，这次仍会再请求微信。", session.BlockedAt.Format("01-02 15:04"), why)
}

func wechatTrace(err error) string {
	var apiErr *ilink.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Message != "" {
			return fmt.Sprintf("微信返回 %d：%s。", apiErr.Code, apiErr.Message)
		}
		return fmt.Sprintf("微信返回 %d，没有附加说明。", apiErr.Code)
	}
	return "请求没有完成：" + err.Error()
}

func ago(d time.Duration) string {
	if d < time.Minute {
		return "刚刚"
	}
	if d < time.Hour {
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	}
	if d < 48*time.Hour {
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	}
	return fmt.Sprintf("%d 天前", int(d.Hours()/24))
}

type sendError struct {
	err   error
	trace string
}

func (e *sendError) Error() string { return e.trace }

func (e *sendError) Unwrap() error { return e.err }

func traceErr(err error, steps []string) error {
	var b strings.Builder
	if err != nil {
		b.WriteString(err.Error())
	}
	for _, step := range steps {
		if strings.TrimSpace(step) == "" {
			continue
		}
		b.WriteString("\n")
		b.WriteString(step)
	}
	return &sendError{err: err, trace: b.String()}
}

func (a *App) Sessions() ([]store.Session, error) {
	return a.Store.ListSessions()
}

func (a *App) pickSession(to string) (store.Session, error) {
	if strings.TrimSpace(to) != "" {
		session, ok, err := a.Store.Session(to)
		if err != nil {
			return store.Session{}, err
		}
		if !ok || session.ContextToken == "" {
			return store.Session{}, fmt.Errorf("没有用户 %s 的会话，请让该用户先给 ClawBot 发一条消息", to)
		}
		return session, nil
	}
	session, ok, err := a.Store.LatestSession()
	if err != nil {
		return store.Session{}, err
	}
	if !ok || session.ContextToken == "" {
		return store.Session{}, ErrNoSession
	}
	return session, nil
}

func (a *App) loggedIn() (store.Credentials, error) {
	creds, err := a.Store.LoadCredentials()
	if err != nil {
		return store.Credentials{}, err
	}
	if creds.BotToken == "" || creds.BaseURL == "" {
		return store.Credentials{}, ErrNotLoggedIn
	}
	return creds, nil
}

func toIlink(creds store.Credentials) ilink.Credentials {
	return ilink.Credentials{
		BotToken: creds.BotToken,
		BaseURL:  creds.BaseURL,
		BotID:    creds.BotID,
		UserID:   creds.UserID,
	}
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
