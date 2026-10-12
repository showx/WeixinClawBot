//go:build windows

package winui

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"
	"github.com/lxn/win"

	. "github.com/lxn/walk/declarative"

	"weixinclawbot/internal/schedule"
)

func (d *desk) tasks() {
	if d.book == nil {
		d.setNotice("定时任务不可用")
		return
	}
	box := &taskBox{owner: d, model: &textModel{}}
	var dlg *walk.Dialog
	var list *walk.ListBox
	var detail *walk.Label
	var runBtn *walk.PushButton
	err := (Dialog{
		AssignTo: &dlg,
		Icon:     appIcon(),
		Title:    "定时任务",
		Font:     Font{Family: "Microsoft YaHei UI", PointSize: 10},
		MinSize:  Size{Width: 720, Height: 460},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: "窗口或 serve 开着才会到点执行。新建的任务不会马上发送。接收者留空则发给该微信最近的会话。"},
			ListBox{
				AssignTo:              &list,
				Model:                 box.model,
				MinSize:               Size{Height: 220},
				OnCurrentIndexChanged: func() { box.onSelect() },
			},
			Label{AssignTo: &detail, Text: "还没有定时任务。"},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					PushButton{Text: "新建", OnClicked: func() { box.create() }},
					PushButton{Text: "编辑", OnClicked: func() { box.edit() }},
					PushButton{Text: "删除", OnClicked: func() { box.remove() }},
					PushButton{AssignTo: &runBtn, Text: "现在运行", OnClicked: func() { box.runNow() }},
					HSpacer{},
					PushButton{Text: "关闭", OnClicked: func() { dlg.Accept() }},
				},
			},
		},
	}).Create(d.mw)
	if err != nil {
		d.setNotice(err.Error())
		return
	}
	box.dlg = dlg
	box.list = list
	box.detail = detail
	box.runBtn = runBtn
	box.reload()

	stop := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(stop) }) }
	go func() {
		ticker := time.NewTicker(2 * time.Second)
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
					box.reload()
				})
			}
		}
	}()
	dlg.Run()
	finish()
}

type taskBox struct {
	owner    *desk
	dlg      *walk.Dialog
	list     *walk.ListBox
	detail   *walk.Label
	runBtn   *walk.PushButton
	model    *textModel
	tasks    []schedule.Task
	suppress bool
	running  bool
	status   string
}

func (b *taskBox) reload() {
	items, err := b.owner.book.List()
	if err != nil {
		b.detail.SetText(err.Error())
		return
	}
	prev := ""
	if i := b.list.CurrentIndex(); i >= 0 && i < len(b.tasks) {
		prev = b.tasks[i].ID
	}
	b.tasks = items
	labels := make([]string, len(items))
	index := -1
	for i, task := range items {
		labels[i] = task.Summary()
		if task.ID == prev {
			index = i
		}
	}
	if index < 0 && len(items) > 0 {
		index = 0
	}
	b.suppress = true
	b.model.items = labels
	b.model.PublishItemsReset()
	_ = b.list.SetCurrentIndex(index)
	b.suppress = false
	b.showDetail()
}

func (b *taskBox) onSelect() {
	if b.suppress {
		return
	}
	b.showDetail()
}

func (b *taskBox) showDetail() {
	if b.running {
		b.detail.SetText("正在运行…")
		return
	}
	if b.status != "" {
		b.detail.SetText(b.status)
		return
	}
	task, ok := b.selected()
	if !ok {
		if len(b.tasks) == 0 {
			b.detail.SetText("还没有定时任务。")
			return
		}
		b.detail.SetText("选一条任务可以查看上次结果。")
		return
	}
	b.detail.SetText(task.Detail())
}

func (b *taskBox) selected() (schedule.Task, bool) {
	index := b.list.CurrentIndex()
	if index < 0 || index >= len(b.tasks) {
		return schedule.Task{}, false
	}
	return b.tasks[index], true
}

func (b *taskBox) create() {
	to := ""
	if b.owner.to != nil {
		to = strings.TrimSpace(b.owner.to.Text())
	}
	current := schedule.Task{
		Account:      b.owner.selected,
		To:           to,
		Enabled:      true,
		Kind:         schedule.KindText,
		Mode:         schedule.ModeDaily,
		At:           "08:30",
		EveryMinutes: 60,
	}
	if editTask(b.dlg, b.owner.book, b.accountNames(current.Account), current) {
		b.reload()
	}
}

func (b *taskBox) edit() {
	current, ok := b.selected()
	if !ok {
		b.detail.SetText("先选一条任务。")
		return
	}
	if editTask(b.dlg, b.owner.book, b.accountNames(current.Account), current) {
		b.reload()
	}
}

func (b *taskBox) remove() {
	current, ok := b.selected()
	if !ok {
		b.detail.SetText("先选一条任务。")
		return
	}
	if walk.MsgBox(b.dlg, "删除定时任务", "删除「"+current.Name+"」？", walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != win.IDYES {
		return
	}
	if err := b.owner.book.Delete(current.ID); err != nil {
		b.detail.SetText(err.Error())
		return
	}
	b.reload()
}

func (b *taskBox) runNow() {
	current, ok := b.selected()
	if !ok {
		b.detail.SetText("先选一条任务。")
		return
	}
	b.running = true
	b.status = ""
	b.runBtn.SetEnabled(false)
	b.detail.SetText("正在运行…")
	id := current.ID
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				b.finishRun("运行中断")
			}
		}()
		_, err := b.owner.book.RunOne(b.owner.ctx, id, b.owner.sendTask)
		if err != nil {
			b.finishRun(err.Error())
			return
		}
		b.finishRun("")
	}()
}

func (b *taskBox) finishRun(message string) {
	chars := []rune(message)
	if len(chars) > 200 {
		message = string(chars[:200])
	}
	b.dlg.Synchronize(func() {
		if b.dlg.IsDisposed() {
			return
		}
		b.running = false
		b.runBtn.SetEnabled(true)
		b.status = message
		b.reload()
	})
}

func (b *taskBox) accountNames(extra string) []string {
	names := make([]string, 0, len(b.owner.accountRows)+1)
	seen := map[string]bool{}
	for _, row := range b.owner.accountRows {
		if row.Name == "" || seen[row.Name] {
			continue
		}
		seen[row.Name] = true
		names = append(names, row.Name)
	}
	if extra != "" && !seen[extra] {
		names = append(names, extra)
	}
	return names
}

func (d *desk) sendTask(ctx context.Context, account, to, text string) error {
	_, err := d.app.SendAccount(ctx, account, to, text)
	return err
}

func editTask(owner walk.Form, book *schedule.Book, accounts []string, current schedule.Task) bool {
	if accounts == nil {
		accounts = []string{}
	}
	var dlg *walk.Dialog
	var nameBox, toBox, atBox, everyBox *walk.LineEdit
	var accountBox, modeBox, kindBox *walk.ComboBox
	var body *walk.TextEdit
	var enabled *walk.CheckBox
	var hint, errLabel, atLabel, everyLabel *walk.Label
	editor := &taskEditor{accounts: accounts}
	title := "编辑定时任务"
	if current.ID == "" {
		title = "新建定时任务"
	}
	everyText := "60"
	if current.EveryMinutes > 0 {
		everyText = strconv.Itoa(current.EveryMinutes)
	}
	err := (Dialog{
		AssignTo: &dlg,
		Icon:     appIcon(),
		Title:    title,
		Font:     Font{Family: "Microsoft YaHei UI", PointSize: 10},
		MinSize:  Size{Width: 460, Height: 560},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: "名称"},
			LineEdit{AssignTo: &nameBox, Text: current.Name, CueBanner: "例如 早报"},
			Label{Text: "微信"},
			ComboBox{AssignTo: &accountBox, Model: accounts},
			Label{Text: "发给谁"},
			LineEdit{AssignTo: &toBox, Text: current.To, CueBanner: "留空则发给这个微信最近的会话"},
			Label{Text: "何时"},
			ComboBox{
				AssignTo:              &modeBox,
				Model:                 []string{"每天", "每隔"},
				OnCurrentIndexChanged: func() { editor.applyMode() },
			},
			Label{AssignTo: &atLabel, Text: "时间"},
			LineEdit{AssignTo: &atBox, Text: current.At, CueBanner: "08:30"},
			Label{AssignTo: &everyLabel, Text: "间隔（分钟）"},
			LineEdit{AssignTo: &everyBox, Text: everyText, CueBanner: "60"},
			Label{Text: "发送什么"},
			ComboBox{
				AssignTo:              &kindBox,
				Model:                 []string{"固定文本", "运行命令", "抓取页面", "接口数据"},
				OnCurrentIndexChanged: func() { editor.applyKind() },
			},
			Label{AssignTo: &hint, Text: "到点把这段文字发给上面的微信。"},
			TextEdit{AssignTo: &body, Text: current.Body, MinSize: Size{Height: 120}, VScroll: true},
			CheckBox{AssignTo: &enabled, Text: "启用", Checked: current.Enabled},
			Label{AssignTo: &errLabel},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					HSpacer{},
					PushButton{Text: "取消", OnClicked: func() { dlg.Cancel() }},
					PushButton{Text: "保存", OnClicked: func() { editor.save() }},
				},
			},
		},
	}).Create(owner)
	if err != nil {
		walk.MsgBox(owner, "定时任务", err.Error(), walk.MsgBoxIconError)
		return false
	}
	editor.dlg = dlg
	editor.book = book
	editor.current = current
	editor.name = nameBox
	editor.to = toBox
	editor.at = atBox
	editor.every = everyBox
	editor.account = accountBox
	editor.mode = modeBox
	editor.kind = kindBox
	editor.body = body
	editor.enabled = enabled
	editor.hint = hint
	editor.errLabel = errLabel
	editor.atLabel = atLabel
	editor.everyLabel = everyLabel
	_ = accountBox.SetCurrentIndex(chosenIndex(accounts, current.Account))
	modeIndex := 0
	if current.Mode == schedule.ModeInterval {
		modeIndex = 1
	}
	_ = modeBox.SetCurrentIndex(modeIndex)
	kindIndex := 0
	switch current.Kind {
	case schedule.KindCommand:
		kindIndex = 1
	case schedule.KindPage:
		kindIndex = 2
	case schedule.KindAPI:
		kindIndex = 3
	}
	_ = kindBox.SetCurrentIndex(kindIndex)
	editor.applyMode()
	editor.applyKind()
	if len(accounts) == 0 {
		errLabel.SetText("还没有微信。先在主窗口添加一个，再保存任务。")
	}
	return dlg.Run() == walk.DlgCmdOK
}

type taskEditor struct {
	dlg        *walk.Dialog
	book       *schedule.Book
	current    schedule.Task
	accounts   []string
	name       *walk.LineEdit
	to         *walk.LineEdit
	at         *walk.LineEdit
	every      *walk.LineEdit
	account    *walk.ComboBox
	mode       *walk.ComboBox
	kind       *walk.ComboBox
	body       *walk.TextEdit
	enabled    *walk.CheckBox
	hint       *walk.Label
	errLabel   *walk.Label
	atLabel    *walk.Label
	everyLabel *walk.Label
}

func (e *taskEditor) applyMode() {
	if e.atLabel == nil || e.mode == nil {
		return
	}
	daily := e.mode.CurrentIndex() != 1
	e.atLabel.SetVisible(daily)
	e.at.SetVisible(daily)
	e.everyLabel.SetVisible(!daily)
	e.every.SetVisible(!daily)
}

func (e *taskEditor) applyKind() {
	if e.hint == nil || e.kind == nil {
		return
	}
	switch e.kind.CurrentIndex() {
	case 1:
		e.hint.SetText("到点运行这条命令，把输出发给上面的微信。命令大约最多跑 45 秒。")
	case 2:
		e.hint.SetText("到点用已经登录的 Chrome 或 Edge 打开网址，等页面和请求都加载完再读出文字，然后关掉这个窗口。")
	case 3:
		e.hint.SetText("第一行写接口地址。下面每行一个字段，例如 data.total 或 data.list.0.name。需要令牌时再加一行 Header: Authorization: Bearer 令牌。")
	default:
		e.hint.SetText("到点把这段文字发给上面的微信。")
	}
}

func (e *taskEditor) save() {
	task := e.current
	task.Name = e.name.Text()
	task.To = e.to.Text()
	task.Body = e.body.Text()
	task.Enabled = e.enabled.Checked()
	task.Account = ""
	if i := e.account.CurrentIndex(); i >= 0 && i < len(e.accounts) {
		task.Account = e.accounts[i]
	}
	if e.mode.CurrentIndex() == 1 {
		task.Mode = schedule.ModeInterval
		minutes, err := strconv.Atoi(strings.TrimSpace(e.every.Text()))
		if err != nil {
			e.errLabel.SetText("间隔要写成分钟数，例如 60")
			return
		}
		task.EveryMinutes = minutes
	} else {
		task.Mode = schedule.ModeDaily
		task.At = e.at.Text()
	}
	switch e.kind.CurrentIndex() {
	case 1:
		task.Kind = schedule.KindCommand
	case 2:
		task.Kind = schedule.KindPage
	case 3:
		task.Kind = schedule.KindAPI
	default:
		task.Kind = schedule.KindText
	}
	if _, err := e.book.Save(task); err != nil {
		e.errLabel.SetText(err.Error())
		return
	}
	e.dlg.Accept()
}

func chosenIndex(items []string, want string) int {
	for i, item := range items {
		if item == want {
			return i
		}
	}
	if len(items) == 0 {
		return -1
	}
	return 0
}
