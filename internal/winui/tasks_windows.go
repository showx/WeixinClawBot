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

func kvAddButton(text string, onClick func()) Widget {
	return Composite{
		Layout: HBox{MarginsZero: true},
		Children: []Widget{
			PushButton{Text: text, OnClicked: onClick},
			HSpacer{},
		},
	}
}

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
	var master *walk.CheckBox
	scheduleOn, err := d.book.SchedulerOn()
	if err != nil {
		d.setNotice(err.Error())
		return
	}
	err = (Dialog{
		AssignTo: &dlg,
		Icon:     appIcon(),
		Title:    "定时任务",
		Font:     Font{Family: "Microsoft YaHei UI", PointSize: 10},
		MinSize:  Size{Width: 720, Height: 460},
		Layout:   VBox{},
		Children: []Widget{
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					CheckBox{AssignTo: &master, Text: "到点执行", Checked: scheduleOn, OnCheckedChanged: func() { box.onMaster() }},
					Label{Text: "关闭后窗口仍监听微信，到点不会发。「现在运行」仍可手动试一次。"},
				},
			},
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
					PushButton{Text: "日志", OnClicked: func() { box.showLogs() }},
					PushButton{Text: "模型", OnClicked: func() { editModel(dlg, d.book) }},
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
	box.master = master
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
	d.syncScheduleSwitch()
}

type taskBox struct {
	owner       *desk
	dlg         *walk.Dialog
	list        *walk.ListBox
	detail      *walk.Label
	runBtn      *walk.PushButton
	master      *walk.CheckBox
	model       *textModel
	tasks       []schedule.Task
	suppress    bool
	running     bool
	status      string
	masterQuiet bool
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
	if b.master != nil {
		on, err := b.owner.book.SchedulerOn()
		if err == nil && b.master.Checked() != on {
			b.masterQuiet = true
			b.master.SetChecked(on)
			b.masterQuiet = false
		}
	}
}

func (b *taskBox) onMaster() {
	if b.masterQuiet || b.master == nil || b.owner == nil || b.owner.book == nil {
		return
	}
	if err := b.owner.book.SetSchedulerOn(b.master.Checked()); err != nil {
		b.owner.setNotice(err.Error())
		return
	}
	if b.master.Checked() {
		b.owner.setNotice("定时任务已打开，到点会发送")
		return
	}
	b.owner.setNotice("定时任务已关闭。窗口仍在监听，到点不会发送。")
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

func (b *taskBox) showLogs() {
	if b.owner == nil || b.owner.book == nil {
		return
	}
	var dlg *walk.Dialog
	var list *walk.ListBox
	var body *walk.TextEdit
	model := &textModel{}
	var entries []schedule.RunLog
	show := func() {
		if body == nil || list == nil {
			return
		}
		index := list.CurrentIndex()
		if index < 0 || index >= len(entries) {
			return
		}
		_ = body.SetText(entries[index].Detail())
	}
	reload := func() {
		logs, err := b.owner.book.Logs()
		if body == nil {
			return
		}
		if err != nil {
			entries = nil
			model.items = nil
			model.PublishItemsReset()
			_ = body.SetText(err.Error())
			return
		}
		entries = logs
		labels := make([]string, len(logs))
		for i, item := range logs {
			labels[i] = item.Line()
		}
		model.items = labels
		model.PublishItemsReset()
		if len(logs) == 0 {
			_ = body.SetText("还没有记录。在主窗口发送、定时任务到点，或对方从手机发来消息之后，会记在这里。")
			return
		}
		_ = list.SetCurrentIndex(0)
		show()
	}
	err := (Dialog{
		AssignTo: &dlg,
		Icon:     appIcon(),
		Title:    "发送日志",
		Font:     Font{Family: "Microsoft YaHei UI", PointSize: 10},
		MinSize:  Size{Width: 720, Height: 520},
		Layout:   VBox{Margins: Margins{Left: 14, Top: 12, Right: 14, Bottom: 12}, Spacing: 8},
		Children: []Widget{
			Label{Text: "和主窗口下方是同一份记录，包括手机发来的消息、即时发送和定时任务。", TextColor: walk.RGB(110, 110, 110)},
			ListBox{
				AssignTo:              &list,
				Model:                 model,
				MinSize:               Size{Height: 180},
				OnCurrentIndexChanged: show,
			},
			TextEdit{AssignTo: &body, ReadOnly: true, VScroll: true, HScroll: true, StretchFactor: 1, Font: Font{Family: "Consolas", PointSize: 10}},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					PushButton{Text: "刷新", OnClicked: reload},
					HSpacer{},
					PushButton{Text: "关闭", OnClicked: func() { dlg.Accept() }},
				},
			},
		},
	}).Create(b.dlg)
	if err != nil {
		walk.MsgBox(b.dlg, "定时任务日志", err.Error(), walk.MsgBoxIconError)
		return
	}
	reload()
	dlg.Run()
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
	var accountBox *walk.ListBox
	var modeBox, kindBox *walk.ComboBox
	var body *walk.TextEdit
	var enabled *walk.CheckBox
	var hint, errLabel, atLabel, everyLabel, apiStatus *walk.Label
	var apiPanel *walk.Composite
	var method *walk.ComboBox
	var urlBox *walk.LineEdit
	var apiBody, response *walk.TextEdit
	var choices *walk.ListBox
	var paramScroll, headerScroll, fieldScroll *walk.ScrollView
	var sendBtn *walk.PushButton
	studio := &apiStudio{syncing: true}
	choiceModel := &textModel{}
	editor := &taskEditor{accounts: accounts, api: studio}
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
		MinSize:  Size{Width: 860, Height: 640},
		Layout:   VBox{Margins: Margins{Left: 16, Top: 12, Right: 16, Bottom: 12}, Spacing: 8},
		Children: []Widget{
			Composite{
				Layout: HBox{MarginsZero: true, Spacing: 8},
				Children: []Widget{
					Label{Text: "名称", MinSize: Size{Width: 52}},
					LineEdit{AssignTo: &nameBox, Text: current.Name, CueBanner: "例如 早报", StretchFactor: 1},
					Label{Text: "发给谁"},
					LineEdit{AssignTo: &toBox, Text: current.To, CueBanner: "留空则每个微信发给自己最近的会话", StretchFactor: 1},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true, Spacing: 8},
				Children: []Widget{
					Label{Text: "微信", MinSize: Size{Width: 52}},
					ListBox{AssignTo: &accountBox, Model: accounts, MultiSelection: true, MinSize: Size{Width: 220, Height: 72}},
					Label{Text: "按住 Ctrl 多选。到点会把同一条内容发给每个选中的微信。", TextColor: walk.RGB(110, 110, 110)},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true, Spacing: 8},
				Children: []Widget{
					Label{Text: "何时", MinSize: Size{Width: 52}},
					ComboBox{
						AssignTo:              &modeBox,
						Model:                 []string{"每天", "每隔"},
						MinSize:               Size{Width: 88},
						OnCurrentIndexChanged: func() { editor.applyMode() },
					},
					Label{AssignTo: &atLabel, Text: "时间"},
					LineEdit{AssignTo: &atBox, Text: current.At, CueBanner: "08:30", MinSize: Size{Width: 76}},
					Label{AssignTo: &everyLabel, Text: "间隔"},
					LineEdit{AssignTo: &everyBox, Text: everyText, CueBanner: "分钟", MinSize: Size{Width: 64}},
					Label{Text: "内容"},
					ComboBox{
						AssignTo:              &kindBox,
						Model:                 []string{"固定文本", "运行命令", "抓取页面", "接口数据", "浏览分析"},
						StretchFactor:         1,
						OnCurrentIndexChanged: func() { editor.applyKind() },
					},
				},
			},
			Label{AssignTo: &hint, Text: "到点把这段文字发给上面的微信。", TextColor: walk.RGB(110, 110, 110)},
			TextEdit{AssignTo: &body, Text: current.Body, StretchFactor: 1, MinSize: Size{Height: 160}, VScroll: true},
			Composite{
				AssignTo:      &apiPanel,
				Visible:       false,
				Layout:        VBox{MarginsZero: true},
				StretchFactor: 1,
				Children: []Widget{
					Composite{
						Layout: HBox{MarginsZero: true, Spacing: 8},
						Children: []Widget{
							ComboBox{AssignTo: &method, Model: []string{"GET", "POST"}, MinSize: Size{Width: 88}},
							LineEdit{AssignTo: &urlBox, CueBanner: "https://example.com/api", StretchFactor: 1, OnTextChanged: func() { studio.onURL() }},
							PushButton{AssignTo: &sendBtn, Text: "发送", MinSize: Size{Width: 84}, OnClicked: func() { studio.send() }},
						},
					},
					TabWidget{
						StretchFactor: 1,
						MinSize:       Size{Height: 210},
						Pages: []TabPage{
							{
								Title:  "参数",
								Layout: VBox{Margins: Margins{Left: 8, Top: 8, Right: 8, Bottom: 8}, Spacing: 6},
								Children: []Widget{
									ScrollView{AssignTo: &paramScroll, Layout: VBox{MarginsZero: true, Spacing: 4, Alignment: AlignHNearVNear}, StretchFactor: 1},
									kvAddButton("添加参数", func() { studio.params.add(schedule.APIRow{Enabled: true}) }),
								},
							},
							{
								Title:  "请求头",
								Layout: VBox{Margins: Margins{Left: 8, Top: 8, Right: 8, Bottom: 8}, Spacing: 6},
								Children: []Widget{
									ScrollView{AssignTo: &headerScroll, Layout: VBox{MarginsZero: true, Spacing: 4, Alignment: AlignHNearVNear}, StretchFactor: 1},
									kvAddButton("添加请求头", func() { studio.headers.add(schedule.APIRow{Enabled: true}) }),
								},
							},
							{
								Title:  "正文",
								Layout: VBox{Margins: Margins{Left: 8, Top: 8, Right: 8, Bottom: 8}, Spacing: 6},
								Children: []Widget{
									Label{Text: "只有 POST 会发送。以 { 或 [ 开头时，自动按 JSON 提交。", TextColor: walk.RGB(110, 110, 110)},
									TextEdit{AssignTo: &apiBody, VScroll: true, StretchFactor: 1, Font: Font{Family: "Consolas", PointSize: 10}},
								},
							},
							{
								Title:  "字段",
								Layout: VBox{Margins: Margins{Left: 8, Top: 8, Right: 8, Bottom: 8}, Spacing: 6},
								Children: []Widget{
									ScrollView{AssignTo: &fieldScroll, Layout: VBox{MarginsZero: true, Spacing: 4, Alignment: AlignHNearVNear}, StretchFactor: 1, MinSize: Size{Height: 72}},
									kvAddButton("添加字段", func() { studio.fields.add(schedule.APIRow{Enabled: true}) }),
									Label{Text: "发送后可从返回里加入", TextColor: walk.RGB(110, 110, 110)},
									ListBox{AssignTo: &choices, Model: choiceModel, MultiSelection: true, MinSize: Size{Height: 64}},
									Composite{
										Layout: HBox{MarginsZero: true, Spacing: 8},
										Children: []Widget{
											PushButton{Text: "加入选中的字段", OnClicked: func() { studio.takeSelected() }},
											PushButton{Text: "全部加入", OnClicked: func() { studio.takeAll() }},
											HSpacer{},
										},
									},
								},
							},
						},
					},
					Composite{
						Layout: HBox{MarginsZero: true, Spacing: 8},
						Children: []Widget{
							Label{Text: "返回", TextColor: walk.RGB(80, 80, 80)},
							Label{AssignTo: &apiStatus, Text: "还没有发送", TextColor: walk.RGB(80, 80, 80)},
							HSpacer{},
						},
					},
					TextEdit{AssignTo: &response, ReadOnly: true, VScroll: true, HScroll: true, StretchFactor: 1, MinSize: Size{Height: 120}, Font: Font{Family: "Consolas", PointSize: 10}},
				},
			},
			CheckBox{AssignTo: &enabled, Text: "启用", Checked: current.Enabled},
			Label{AssignTo: &errLabel},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					PushButton{Text: "模型", OnClicked: func() {
						editModel(dlg, book)
						editor.applyKind()
					}},
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
	editor.apiPanel = apiPanel
	studio.owner = dlg
	studio.method = method
	studio.url = urlBox
	studio.body = apiBody
	studio.status = apiStatus
	studio.response = response
	studio.choices = choices
	studio.choiceModel = choiceModel
	studio.sendBtn = sendBtn
	studio.params = newKVList(paramScroll, "参数名", "参数值", studio.onParams)
	studio.headers = newKVList(headerScroll, "名称", "内容", nil)
	studio.fields = newKVList(fieldScroll, "显示名", "路径", nil)
	accountBox.SetSelectedIndexes(chosenAccountIndexes(accounts, current))
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
	case schedule.KindBrowse:
		kindIndex = 4
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
	account    *walk.ListBox
	mode       *walk.ComboBox
	kind       *walk.ComboBox
	body       *walk.TextEdit
	enabled    *walk.CheckBox
	hint       *walk.Label
	errLabel   *walk.Label
	atLabel    *walk.Label
	everyLabel *walk.Label
	apiPanel   *walk.Composite
	api        *apiStudio
	apiLoaded  bool
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
	api := e.kind.CurrentIndex() == 3
	e.showAPI(api)
	if api {
		e.hint.SetText("不勾选字段时，把返回的 JSON 直接发给微信。勾选后只发选中的路径。{today}、{yesterday} 换成 20261014 这种；{today-yyyy-mm-dd}、{yesterday-yyyy-mm-dd} 换成 2026-10-14 这种。")
		return
	}
	switch e.kind.CurrentIndex() {
	case 1:
		e.hint.SetText("到点运行这条命令，把输出发给选中的微信。命令大约最多跑 45 秒。")
	case 2:
		e.hint.SetText("到点用已经登录的 Chrome 或 Edge 打开网址，等页面和请求都加载完再读出文字，然后关掉这个窗口。")
	case 4:
		text := "第一行写网址，下面写要回答的问题。到点在后台打开页面，让模型按这个问题查看、点击或填写，再把结论发出去。"
		if e.book != nil {
			cfg, err := schedule.LoadAI(e.book.Dir())
			if err != nil || cfg.Ready() != nil {
				text += "请先点「模型」填写接口。"
			}
		}
		e.hint.SetText(text)
	default:
		e.hint.SetText("到点把这段文字发给选中的微信。")
	}
}

func (e *taskEditor) save() {
	task := e.current
	task.Name = e.name.Text()
	task.To = e.to.Text()
	task.Enabled = e.enabled.Checked()
	if e.kind.CurrentIndex() == 3 {
		if e.api == nil {
			e.errLabel.SetText("请求还没有准备好")
			return
		}
		text, err := e.api.form(false)
		if err != nil {
			e.errLabel.SetText(err.Error())
			return
		}
		task.Body = text
	} else {
		task.Body = e.body.Text()
	}
	var selected []string
	if e.account != nil {
		for _, index := range e.account.SelectedIndexes() {
			if index >= 0 && index < len(e.accounts) {
				selected = append(selected, e.accounts[index])
			}
		}
	}
	task.Accounts = selected
	task.Account = ""
	if len(selected) > 0 {
		task.Account = selected[0]
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
	case 4:
		task.Kind = schedule.KindBrowse
	default:
		task.Kind = schedule.KindText
	}
	if _, err := e.book.Save(task); err != nil {
		e.errLabel.SetText(err.Error())
		return
	}
	e.dlg.Accept()
}

func chosenAccountIndexes(items []string, task schedule.Task) []int {
	selected := map[string]bool{}
	for _, name := range task.AccountNames() {
		selected[name] = true
	}
	var indexes []int
	for i, item := range items {
		if selected[item] {
			indexes = append(indexes, i)
		}
	}
	return indexes
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

func (e *taskEditor) showAPI(on bool) {
	if e.apiPanel == nil || e.body == nil {
		return
	}
	e.apiPanel.SetVisible(on)
	e.body.SetVisible(!on)
	if on {
		e.loadAPIForm()
	}
	if e.dlg != nil {
		e.dlg.RequestLayout()
	}
}

func (e *taskEditor) loadAPIForm() {
	if e.apiLoaded || e.api == nil || e.body == nil {
		return
	}
	e.apiLoaded = true
	e.api.load(e.body.Text())
}

func editModel(owner walk.Form, book *schedule.Book) {
	if book == nil {
		return
	}
	cfg, err := schedule.LoadAI(book.Dir())
	if err != nil {
		walk.MsgBox(owner, "模型", err.Error(), walk.MsgBoxIconError)
		return
	}
	var dlg *walk.Dialog
	var baseBox, keyBox, modelBox *walk.LineEdit
	var errLabel *walk.Label
	err = (Dialog{
		AssignTo: &dlg,
		Icon:     appIcon(),
		Title:    "模型",
		Font:     Font{Family: "Microsoft YaHei UI", PointSize: 10},
		MinSize:  Size{Width: 460, Height: 280},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: "接口地址"},
			LineEdit{AssignTo: &baseBox, Text: cfg.Base, CueBanner: "https://api.openai.com/v1"},
			Label{Text: "密钥"},
			LineEdit{AssignTo: &keyBox, Text: cfg.Key, PasswordMode: true},
			Label{Text: "模型名"},
			LineEdit{AssignTo: &modelBox, Text: cfg.Model, CueBanner: "gpt-4o-mini"},
			Label{Text: "填写兼容 OpenAI 的接口。地址写到 /v1 为止，不要把密钥写进地址。"},
			Label{AssignTo: &errLabel},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					HSpacer{},
					PushButton{Text: "取消", OnClicked: func() { dlg.Cancel() }},
					PushButton{Text: "保存", OnClicked: func() {
						err := schedule.SaveAI(book.Dir(), schedule.AIConfig{
							Base:  baseBox.Text(),
							Key:   keyBox.Text(),
							Model: modelBox.Text(),
						})
						if err != nil {
							errLabel.SetText(err.Error())
							return
						}
						dlg.Accept()
					}},
				},
			},
		},
	}).Create(owner)
	if err != nil {
		walk.MsgBox(owner, "模型", err.Error(), walk.MsgBoxIconError)
		return
	}
	dlg.Run()
}
