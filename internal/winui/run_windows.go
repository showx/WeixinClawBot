//go:build windows

package winui

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image/png"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"
	"github.com/lxn/win"

	. "github.com/lxn/walk/declarative"

	"weixinclawbot/internal/app"
	"weixinclawbot/internal/schedule"
)

func appIcon() *walk.Icon {
	icon, err := walk.NewIconFromResourceId(1)
	if err != nil {
		return nil
	}
	return icon
}

// Run 打开 Windows 桌面窗口，并在后台监听已登录的微信。
func Run(application *app.App, listen, token string) error {
	err := run(application, listen, token)
	if err != nil {
		walk.MsgBox(nil, "微信 ClawBot", err.Error(), walk.MsgBoxIconError)
	}
	return err
}

func run(application *app.App, listen, token string) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	addr := hostPort(ln.Addr().String())
	srv := &http.Server{
		Handler:           (&app.Server{App: application, Token: token, Listen: addr}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ln) }()
	go func() { _ = application.Supervise(ctx) }()
	var book *schedule.Book
	if application.Catalog != nil {
		book, err = schedule.Open(application.Catalog.Dir())
		if err != nil {
			return err
		}
		go book.Loop(ctx, func(ctx context.Context, account, to, text string) error {
			_, err := application.SendAccount(ctx, account, to, text)
			return err
		})
	}
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = srv.Shutdown(shutdown)
	}()

	return openWindow(application, book, ctx, addr, token)
}

func openWindow(application *app.App, book *schedule.Book, ctx context.Context, addr, token string) error {
	ui := &desk{
		app:      application,
		book:     book,
		ctx:      ctx,
		addr:     addr,
		token:    token,
		accounts: &textModel{},
		sessions: &textModel{},
	}
	if err := (MainWindow{
		AssignTo: &ui.mw,
		Icon:     appIcon(),
		Title:    "微信 ClawBot",
		Font:     Font{Family: "Microsoft YaHei UI", PointSize: 10},
		MinSize:  Size{Width: 860, Height: 560},
		Size:     Size{Width: 960, Height: 640},
		Layout:   VBox{Margins: Margins{Left: 12, Top: 12, Right: 12, Bottom: 12}},
		Children: []Widget{
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					Label{Text: "扫码、查看会话、发消息。窗口开着就会监听微信，并执行定时任务。"},
					HSpacer{},
					PushButton{Text: "定时任务", OnClicked: ui.tasks},
					Label{AssignTo: &ui.watch},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					Composite{
						Layout:    VBox{MarginsZero: true},
						MinSize:   Size{Width: 240},
						MaxSize:   Size{Width: 300},
						Alignment: AlignHNearVNear,
						Children: []Widget{
							Composite{
								Layout: HBox{MarginsZero: true},
								Children: []Widget{
									Label{Text: "微信"},
									HSpacer{},
									PushButton{Text: "添加微信", OnClicked: func() { ui.login("") }},
								},
							},
							ListBox{
								AssignTo:              &ui.accountList,
								Model:                 ui.accounts,
								StretchFactor:         1,
								OnCurrentIndexChanged: ui.onAccount,
							},
							Composite{
								Layout: HBox{MarginsZero: true},
								Children: []Widget{
									PushButton{Text: "重新登录", OnClicked: ui.relogin},
									PushButton{Text: "移除", OnClicked: ui.remove},
								},
							},
						},
					},
					Composite{
						Layout: VBox{MarginsZero: true},
						Children: []Widget{
							Label{AssignTo: &ui.sessionTitle, Text: "会话"},
							ListBox{
								AssignTo:              &ui.sessionList,
								Model:                 ui.sessions,
								StretchFactor:         1,
								OnCurrentIndexChanged: ui.onSession,
							},
							Label{AssignTo: &ui.detail, Text: "还没有会话。"},
							Label{Text: "发给谁"},
							LineEdit{AssignTo: &ui.to, CueBanner: "留空则发给这个微信最近收到消息的用户"},
							Label{Text: "内容"},
							TextEdit{AssignTo: &ui.text, MinSize: Size{Height: 72}},
							Composite{
								Layout: HBox{MarginsZero: true},
								Children: []Widget{
									PushButton{AssignTo: &ui.sendBtn, Text: "发送", OnClicked: ui.send},
									TextEdit{AssignTo: &ui.notice, ReadOnly: true, StretchFactor: 1},
								},
							},
						},
					},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					Label{Text: "本机接口"},
					LineEdit{AssignTo: &ui.apiAddr, ReadOnly: true, Text: "http://" + addr + "/"},
					PushButton{Text: "复制令牌", OnClicked: ui.copyToken},
				},
			},
		},
	}).Create(); err != nil {
		return err
	}
	ui.refresh()
	stop := make(chan struct{})
	go ui.poll(stop)
	ui.mw.Run()
	close(stop)
	return nil
}

type accountItem struct {
	Name     string
	UserID   string
	Online   bool
	Watching bool
}

type sessionItem struct {
	User   string
	Detail string
	When   string
	Status string
}

type textModel struct {
	walk.ListModelBase
	items []string
}

func (m *textModel) ItemCount() int              { return len(m.items) }
func (m *textModel) Value(index int) interface{} { return m.items[index] }

type desk struct {
	app          *app.App
	book         *schedule.Book
	ctx          context.Context
	addr         string
	token        string
	mw           *walk.MainWindow
	watch        *walk.Label
	accountList  *walk.ListBox
	sessionList  *walk.ListBox
	sessionTitle *walk.Label
	detail       *walk.Label
	to           *walk.LineEdit
	text         *walk.TextEdit
	sendBtn      *walk.PushButton
	notice       *walk.TextEdit
	apiAddr      *walk.LineEdit
	accounts     *textModel
	sessions     *textModel
	accountRows  []accountItem
	sessionRows  []sessionItem
	selected     string
	suppress     bool
}

func (d *desk) poll(stop <-chan struct{}) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			d.mw.Synchronize(func() {
				if d.mw.IsDisposed() {
					return
				}
				d.refresh()
			})
		}
	}
}

func (d *desk) refresh() {
	grouped, err := d.app.AccountSessions("")
	if err != nil {
		d.setNotice(err.Error())
		return
	}
	online := map[string]bool{}
	if d.app.Catalog != nil {
		ready, err := d.app.Catalog.LoggedIn()
		if err != nil {
			d.setNotice(err.Error())
			return
		}
		for _, item := range ready {
			online[item.Account.Name] = true
		}
	}
	watching := map[string]bool{}
	for _, name := range d.app.Watching() {
		watching[name] = true
	}
	rows := make([]accountItem, 0, len(grouped))
	labels := make([]string, 0, len(grouped))
	for _, group := range grouped {
		name := group.Account.Name
		row := accountItem{Name: name, UserID: group.Account.UserID, Online: online[name], Watching: watching[name]}
		rows = append(rows, row)
		labels = append(labels, accountLabel(row, len(group.Sessions)))
	}
	d.accountRows = rows
	if len(watching) == 0 {
		d.watch.SetText("尚未监听")
	} else {
		d.watch.SetText(fmt.Sprintf("正在监听 %d 个微信", len(watching)))
	}
	index := 0
	found := false
	for i, row := range rows {
		if row.Name == d.selected {
			index = i
			found = true
			break
		}
	}
	if !found && len(rows) > 0 {
		d.selected = rows[0].Name
	}
	if len(rows) == 0 {
		d.selected = ""
	}
	outer := d.suppress
	d.suppress = true
	d.accounts.items = labels
	d.accounts.PublishItemsReset()
	if len(rows) == 0 {
		d.accountList.SetCurrentIndex(-1)
	} else {
		d.accountList.SetCurrentIndex(index)
	}
	d.fillSessions()
	d.suppress = outer
}

func (d *desk) onAccount() {
	if d.suppress {
		return
	}
	index := d.accountList.CurrentIndex()
	if index < 0 || index >= len(d.accountRows) {
		return
	}
	d.selected = d.accountRows[index].Name
	d.fillSessions()
}

func (d *desk) fillSessions() {
	outer := d.suppress
	d.suppress = true
	defer func() { d.suppress = outer }()

	var current *accountItem
	for i := range d.accountRows {
		if d.accountRows[i].Name == d.selected {
			current = &d.accountRows[i]
			break
		}
	}
	if current == nil {
		d.sessionTitle.SetText("会话")
		d.sessionRows = nil
		d.sessions.items = nil
		d.sessions.PublishItemsReset()
		d.sessionList.SetCurrentIndex(-1)
		d.detail.SetText("先添加一个微信。")
		d.sendBtn.SetEnabled(false)
		return
	}
	d.sessionTitle.SetText("用「" + current.Name + "」发消息")
	d.sendBtn.SetEnabled(true)
	grouped, err := d.app.AccountSessions(current.Name)
	if err != nil {
		d.detail.SetText(err.Error())
		return
	}
	now := time.Now()
	var sessions []sessionItem
	var labels []string
	if len(grouped) > 0 {
		for _, item := range grouped[0].Sessions {
			view := app.DescribeSession(item, now)
			row := sessionItem{
				User:   view.UserID,
				Detail: view.Detail,
				When:   formatTime(view.UpdatedAt),
				Status: sessionMark(view.Status),
			}
			sessions = append(sessions, row)
			labels = append(labels, row.User+"    "+row.Status)
		}
	}
	prev := d.sessionList.CurrentIndex()
	d.sessionRows = sessions
	d.sessions.items = labels
	d.sessions.PublishItemsReset()
	switch {
	case len(sessions) == 0:
		d.sessionList.SetCurrentIndex(-1)
		d.detail.SetText("还没有会话。让对方给这个 ClawBot 发一条任意消息。")
	case prev >= 0 && prev < len(sessions):
		d.sessionList.SetCurrentIndex(prev)
		d.showDetail(prev)
	default:
		d.sessionList.SetCurrentIndex(0)
		d.showDetail(0)
	}
}

func (d *desk) onSession() {
	if d.suppress {
		return
	}
	index := d.sessionList.CurrentIndex()
	if index < 0 || index >= len(d.sessionRows) {
		return
	}
	d.to.SetText(d.sessionRows[index].User)
	d.showDetail(index)
}

func (d *desk) showDetail(index int) {
	if index < 0 || index >= len(d.sessionRows) {
		return
	}
	row := d.sessionRows[index]
	d.detail.SetText(row.Detail + "    上次收到 " + row.When)
}

func (d *desk) send() {
	account := d.selected
	to := strings.TrimSpace(d.to.Text())
	text := strings.TrimSpace(d.text.Text())
	if account == "" {
		d.setNotice("先选择一个微信")
		return
	}
	if text == "" {
		d.setNotice("消息内容不能为空")
		return
	}
	d.sendBtn.SetEnabled(false)
	d.setNotice("正在发送…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := d.app.SendAccount(ctx, account, to, text)
		d.mw.Synchronize(func() {
			if d.mw.IsDisposed() {
				return
			}
			d.sendBtn.SetEnabled(d.selected != "")
			if err != nil {
				d.setNotice(err.Error())
				return
			}
			d.text.SetText("")
			d.setNotice(fmt.Sprintf("已由微信 %s 发送给 %s（本次 %d 条）", result.Account, result.To, result.Parts))
			d.refresh()
		})
	}()
}

func (d *desk) relogin() {
	if d.selected == "" {
		d.login("")
		return
	}
	d.login(d.selected)
}

func (d *desk) remove() {
	name := d.selected
	if name == "" || d.app.Catalog == nil {
		return
	}
	if walk.MsgBox(d.mw, "移除微信", "移除「"+name+"」？\n不再监听它。再次扫码登录同一个微信会恢复原来的会话。", walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != win.IDYES {
		return
	}
	if err := d.app.Catalog.Remove(name); err != nil {
		d.setNotice(err.Error())
		return
	}
	d.selected = ""
	d.refresh()
}

func (d *desk) copyToken() {
	if err := walk.Clipboard().SetText(d.token); err != nil {
		d.setNotice(err.Error())
		return
	}
	d.setNotice("已复制令牌")
}

func (d *desk) setNotice(text string) {
	_ = d.notice.SetText(text)
}

func (d *desk) login(preset string) {
	view := d.app.LoginView()
	if view.Phase != "idle" && view.Phase != "done" && view.Phase != "error" {
		d.showLogin(preset, false)
		return
	}
	if view.Phase == "done" || view.Phase == "error" {
		d.app.CancelLogin()
	}
	d.showLogin(preset, preset != "")
}

func (d *desk) showLogin(preset string, autostart bool) {
	var dlg *walk.Dialog
	var nameBox *walk.LineEdit
	var message *walk.Label
	var qr *walk.ImageView
	var codeBox *walk.LineEdit
	var codeBtn *walk.PushButton
	var startBtn *walk.PushButton
	var closeBtn *walk.PushButton
	box := &loginBox{app: d.app}
	err := (Dialog{
		AssignTo: &dlg,
		Icon:     appIcon(),
		Title:    "添加微信",
		Font:     Font{Family: "Microsoft YaHei UI", PointSize: 10},
		MinSize:  Size{Width: 420, Height: 520},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: "名称"},
			LineEdit{AssignTo: &nameBox, CueBanner: "例如 工作", Text: preset},
			Label{AssignTo: &message, Text: "填写名称后开始扫码。对方要先给这个 ClawBot 发一条消息，才能主动发给对方。"},
			ImageView{
				AssignTo:   &qr,
				MinSize:    Size{Width: 280, Height: 280},
				Mode:       ImageViewModeZoom,
				Background: SolidColorBrush{Color: walk.RGB(255, 255, 255)},
			},
			LineEdit{AssignTo: &codeBox, CueBanner: "手机上显示的数字", Visible: false},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					PushButton{AssignTo: &closeBtn, Text: "关闭", OnClicked: func() { box.close(dlg) }},
					HSpacer{},
					PushButton{AssignTo: &codeBtn, Text: "确认数字", Visible: false, OnClicked: func() { box.submit(codeBox, message) }},
					PushButton{AssignTo: &startBtn, Text: "开始扫码", OnClicked: func() { box.start(nameBox, message) }},
				},
			},
		},
	}).Create(d.mw)
	if err != nil {
		d.setNotice(err.Error())
		return
	}
	box.dlg = dlg
	box.name = nameBox
	box.message = message
	box.qr = qr
	box.code = codeBox
	box.codeBtn = codeBtn
	box.startBtn = startBtn
	box.closeBtn = closeBtn
	defer box.disposeImage()
	if autostart {
		box.start(nameBox, message)
	}
	stop := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(stop) }) }
	go func() {
		ticker := time.NewTicker(700 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				dlg.Synchronize(func() {
					if dlg.IsDisposed() {
						return
					}
					box.paint()
				})
			}
		}
	}()
	dlg.Run()
	finish()
	d.app.CancelLogin()
	d.refresh()
}

type loginBox struct {
	app      *app.App
	dlg      *walk.Dialog
	name     *walk.LineEdit
	message  *walk.Label
	qr       *walk.ImageView
	code     *walk.LineEdit
	codeBtn  *walk.PushButton
	startBtn *walk.PushButton
	closeBtn *walk.PushButton
	image    *walk.Bitmap
	png      []byte
}

func (b *loginBox) start(nameBox *walk.LineEdit, message *walk.Label) {
	if err := b.app.BeginLogin(nameBox.Text()); err != nil {
		message.SetText(err.Error())
		return
	}
	message.SetText("正在获取二维码…")
	b.paint()
}

func (b *loginBox) submit(codeBox *walk.LineEdit, message *walk.Label) {
	if err := b.app.SubmitLoginCode(codeBox.Text()); err != nil {
		message.SetText(err.Error())
		return
	}
	message.SetText("正在核对数字…")
}

func (b *loginBox) close(dlg *walk.Dialog) {
	b.app.CancelLogin()
	dlg.Accept()
}

func (b *loginBox) paint() {
	view := b.app.LoginView()
	if view.Message != "" {
		b.message.SetText(view.Message)
	}
	running := view.Phase == "starting" || view.Phase == "qr" || view.Phase == "scanned" || view.Phase == "verify"
	b.name.SetEnabled(!running)
	b.startBtn.SetVisible(!running && view.Phase != "done")
	verify := view.Phase == "verify"
	b.code.SetVisible(verify)
	b.codeBtn.SetVisible(verify)
	if view.Phase == "done" {
		_ = b.closeBtn.SetText("完成")
	} else {
		_ = b.closeBtn.SetText("关闭")
	}
	pngBytes := loginPNG(view.QR)
	if bytes.Equal(b.png, pngBytes) {
		return
	}
	b.png = append([]byte(nil), pngBytes...)
	b.disposeImage()
	if len(pngBytes) == 0 {
		_ = b.qr.SetImage(nil)
		return
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		b.message.SetText(err.Error())
		return
	}
	bmp, err := walk.NewBitmapFromImage(img)
	if err != nil {
		b.message.SetText(err.Error())
		return
	}
	b.image = bmp
	_ = b.qr.SetImage(bmp)
}

func (b *loginBox) disposeImage() {
	if b.image != nil {
		b.image.Dispose()
		b.image = nil
	}
}

func loginPNG(dataURL string) []byte {
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(dataURL, prefix) {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, prefix))
	if err != nil {
		return nil
	}
	return raw
}

func accountLabel(item accountItem, sessions int) string {
	state := "登录已失效"
	if item.Online {
		state = "已登录"
	}
	if item.Watching {
		state += " · 监听中"
	}
	return fmt.Sprintf("%s    %s    %d 个会话", item.Name, state, sessions)
}

func sessionMark(status string) string {
	switch status {
	case "ready":
		return "可发送"
	case "rejected":
		return "已拒绝"
	case "rate_limited":
		return "已限流"
	default:
		return status
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func hostPort(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "::", "0.0.0.0", "[::]":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
