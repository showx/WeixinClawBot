//go:build windows

package winui

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"

	. "github.com/lxn/walk/declarative"

	"weixinclawbot/internal/schedule"
)

// fitDialog 让窗口不超过当前屏幕的工作区，避免编辑时把窗口拖来拖去。
func fitDialog(dlg *walk.Dialog) {
	if dlg == nil {
		return
	}
	var info win.MONITORINFO
	info.CbSize = uint32(unsafe.Sizeof(info))
	if !win.GetMonitorInfo(win.MonitorFromWindow(dlg.Handle(), win.MONITOR_DEFAULTTONEAREST), &info) {
		return
	}
	limitW := int(info.RcWork.Right-info.RcWork.Left) - dlg.IntFrom96DPI(12)
	limitH := int(info.RcWork.Bottom-info.RcWork.Top) - dlg.IntFrom96DPI(12)
	if limitW < dlg.IntFrom96DPI(480) {
		limitW = dlg.IntFrom96DPI(480)
	}
	if limitH < dlg.IntFrom96DPI(320) {
		limitH = dlg.IntFrom96DPI(320)
	}
	min := dlg.MinSizePixels()
	if min.Width > limitW {
		min.Width = limitW
	}
	if min.Height > limitH {
		min.Height = limitH
	}
	want := walk.Size{
		Width:  dlg.IntFrom96DPI(760),
		Height: dlg.IntFrom96DPI(520),
	}
	if want.Width > limitW {
		want.Width = limitW
	}
	if want.Height > limitH {
		want.Height = limitH
	}
	if want.Width < min.Width {
		want.Width = min.Width
	}
	if want.Height < min.Height {
		want.Height = min.Height
	}
	_ = dlg.SetMinMaxSizePixels(want, walk.Size{Width: limitW, Height: limitH})
}

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
					PushButton{Text: "复制", OnClicked: func() { box.duplicate() }},
					PushButton{Text: "删除", OnClicked: func() { box.remove() }},
					PushButton{AssignTo: &runBtn, Text: "现在运行", OnClicked: func() { box.runNow() }},
					PushButton{Text: "日志", OnClicked: func() { box.showLogs() }},
					PushButton{Text: "宏", OnClicked: func() { editMacros(dlg, d.book) }},
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
		labels[i] = schedule.TaskLabel(i+1, task)
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

func (b *taskBox) duplicate() {
	current, ok := b.selected()
	if !ok {
		b.detail.SetText("先选一条任务。")
		return
	}
	if editTask(b.dlg, b.owner.book, b.accountNames(current.Account), current.Copy()) {
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
	var list *walk.TableView
	var body *walk.TextEdit
	model := &logTable{}
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
			model.rows = nil
			model.PublishRowsReset()
			_ = body.SetText(err.Error())
			return
		}
		entries = logs
		model.rows = logs
		model.PublishRowsReset()
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
			Label{Text: "和主窗口下方是同一份记录，包括手机发来的消息、即时发送和定时任务。点一行，下面看全文。", TextColor: walk.RGB(110, 110, 110)},
			TableView{
				AssignTo:                 &list,
				Model:                    model,
				AlternatingRowBG:         true,
				LastColumnStretched:      true,
				NotSortableByHeaderClick: true,
				ColumnsSizable:           true,
				MinSize:                  Size{Height: 180},
				Font:                     Font{Family: "Microsoft YaHei UI", PointSize: 9},
				OnCurrentIndexChanged:    show,
				Columns: []TableViewColumn{
					{Title: "时间", Width: 108},
					{Title: "来源", Width: 52},
					{Title: "名称", Width: 160},
					{Title: "发给", Width: 140},
					{Title: "结果", Width: 220},
				},
			},
			TextEdit{AssignTo: &body, ReadOnly: true, VScroll: true, HScroll: true, StretchFactor: 1, Font: Font{Family: "Microsoft YaHei UI", PointSize: 10}},
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
	if list != nil {
		list.SetGridlines(true)
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
	var nameBox, codeBox, toBox, atBox, everyBox *walk.LineEdit
	var accountBox *walk.ListBox
	var modeBox, kindBox *walk.ComboBox
	var body *walk.TextEdit
	var enabled, showTime, showName *walk.CheckBox
	var prefixBox *walk.LineEdit
	var hint, errLabel, atLabel, everyLabel, apiStatus *walk.Label
	var apiPanel *walk.Composite
	var method *walk.ComboBox
	var urlBox *walk.LineEdit
	var apiBody, response *walk.TextEdit
	var choices *walk.ListBox
	var paramScroll, headerScroll, fieldScroll *walk.Composite
	var sendBtn *walk.PushButton
	studio := &apiStudio{syncing: true}
	choiceModel := &textModel{}
	editor := &taskEditor{accounts: accounts, api: studio}
	title := "编辑定时任务"
	if current.ID == "" {
		title = "新建定时任务"
		if strings.TrimSpace(current.Name) != "" {
			title = "复制定时任务"
		}
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
		MinSize:  Size{Width: 720, Height: 420},
		Layout:   VBox{Margins: Margins{Left: 12, Top: 10, Right: 12, Bottom: 10}, Spacing: 6},
		Children: []Widget{
			Composite{
				Layout: HBox{MarginsZero: true, Spacing: 8},
				Children: []Widget{
					Label{Text: "名称", MinSize: Size{Width: 52}},
					LineEdit{AssignTo: &nameBox, Text: current.Name, CueBanner: "例如 早报", StretchFactor: 1},
					Label{Text: "查询号"},
					LineEdit{AssignTo: &codeBox, Text: current.Code, CueBanner: "发这个会立刻重跑", MinSize: Size{Width: 140}},
					Label{Text: "发给谁"},
					LineEdit{AssignTo: &toBox, Text: current.To, CueBanner: "留空则每个微信发给自己最近的会话", StretchFactor: 1},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true, Spacing: 8},
				Children: []Widget{
					Label{Text: "微信", MinSize: Size{Width: 52}},
					ListBox{AssignTo: &accountBox, Model: accounts, MultiSelection: true, MinSize: Size{Width: 180, Height: 52}},
					Label{Text: "按住 Ctrl 多选。到点会把同一条内容发给每个选中的微信。", TextColor: walk.RGB(110, 110, 110), MaxSize: Size{Width: 360}},
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
			ScrollView{
				HorizontalFixed: true,
				StretchFactor:   1,
				MinSize:         Size{Height: 160},
				MaxSize:         Size{Width: 700},
				Layout:          VBox{MarginsZero: true, Spacing: 6},
				Children: []Widget{
					Label{AssignTo: &hint, Text: "到点把这段文字发给上面的微信。", TextColor: walk.RGB(110, 110, 110), MaxSize: Size{Width: 680}},
					TextEdit{AssignTo: &body, Text: current.Body, StretchFactor: 1, MinSize: Size{Height: 120}, VScroll: true},
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
								MinSize:       Size{Height: 160},
								Pages: []TabPage{
									{
										Title:  "参数",
										Layout: VBox{Margins: Margins{Left: 8, Top: 8, Right: 8, Bottom: 8}, Spacing: 6},
										Children: []Widget{
											Composite{AssignTo: &paramScroll, Layout: VBox{MarginsZero: true, Spacing: 4, Alignment: AlignHNearVNear}},
											kvAddButton("添加参数", func() { studio.params.add(schedule.APIRow{Enabled: true}) }),
										},
									},
									{
										Title:  "请求头",
										Layout: VBox{Margins: Margins{Left: 8, Top: 8, Right: 8, Bottom: 8}, Spacing: 6},
										Children: []Widget{
											Composite{AssignTo: &headerScroll, Layout: VBox{MarginsZero: true, Spacing: 4, Alignment: AlignHNearVNear}},
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
											Composite{AssignTo: &fieldScroll, Layout: VBox{MarginsZero: true, Spacing: 4, Alignment: AlignHNearVNear}},
											Composite{
												Layout: HBox{MarginsZero: true, Spacing: 8},
												Children: []Widget{
													PushButton{Text: "添加字段", OnClicked: func() { studio.addField() }},
													Label{Text: "单位接在后面：data.list.*.amount元；分先换算：data.list.*.cost/100元", TextColor: walk.RGB(110, 110, 110), MaxSize: Size{Width: 420}},
													HSpacer{},
												},
											},
											Label{Text: "发送后可从返回里加入", TextColor: walk.RGB(110, 110, 110)},
											ListBox{AssignTo: &choices, Model: choiceModel, MultiSelection: true, MinSize: Size{Height: 72}, MaxSize: Size{Height: 72}},
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
							TextEdit{AssignTo: &response, ReadOnly: true, VScroll: true, HScroll: true, StretchFactor: 1, MinSize: Size{Height: 96}, Font: Font{Family: "Consolas", PointSize: 10}},
						},
					},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true, Spacing: 8},
				Children: []Widget{
					Label{Text: "附带"},
					CheckBox{AssignTo: &showTime, Text: "发送时间", Checked: current.ShowTime},
					CheckBox{AssignTo: &showName, Text: "任务名称", Checked: current.ShowName},
					Label{Text: "附言"},
					LineEdit{AssignTo: &prefixBox, Text: current.Prefix, CueBanner: "可选，写在正文最前面", StretchFactor: 1},
				},
			},
			CheckBox{AssignTo: &enabled, Text: "启用", Checked: current.Enabled},
			Label{AssignTo: &errLabel},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					PushButton{Text: "宏", OnClicked: func() { editMacros(dlg, book) }},
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
	editor.code = codeBox
	editor.to = toBox
	editor.at = atBox
	editor.every = everyBox
	editor.account = accountBox
	editor.mode = modeBox
	editor.kind = kindBox
	editor.body = body
	editor.enabled = enabled
	editor.showTime = showTime
	editor.showName = showName
	editor.prefix = prefixBox
	studio.preface = editor.preface
	editor.hint = hint
	editor.errLabel = errLabel
	editor.atLabel = atLabel
	editor.everyLabel = everyLabel
	editor.apiPanel = apiPanel
	if book != nil {
		studio.dir = book.Dir()
	}
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
	fitDialog(dlg)
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
	code       *walk.LineEdit
	to         *walk.LineEdit
	at         *walk.LineEdit
	every      *walk.LineEdit
	account    *walk.ListBox
	mode       *walk.ComboBox
	kind       *walk.ComboBox
	body       *walk.TextEdit
	enabled    *walk.CheckBox
	showTime   *walk.CheckBox
	showName   *walk.CheckBox
	prefix     *walk.LineEdit
	hint       *walk.Label
	errLabel   *walk.Label
	atLabel    *walk.Label
	everyLabel *walk.Label
	apiPanel   *walk.Composite
	api        *apiStudio
	apiLoaded  bool
}

func (e *taskEditor) preface() string {
	if e == nil {
		return ""
	}
	name := ""
	if e.name != nil {
		name = e.name.Text()
	}
	showTime := e.showTime != nil && e.showTime.Checked()
	showName := e.showName != nil && e.showName.Checked()
	prefix := ""
	if e.prefix != nil {
		prefix = e.prefix.Text()
	}
	return schedule.MessagePreface(name, showTime, showName, prefix, time.Now())
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
		e.hint.SetText("不勾选字段时，把返回的 JSON 直接发给微信。勾选后只发选中的路径。同一名单的几列会排成一行。单位直接接在字段后面，例如 data.list.*.amount元。金额如果是分，先写 /100 再写单位，例如 data.list.*.cost/100元；要乘以某个数就写成 *100。{today}、{yesterday} 换成 20261014 这种；{today-yyyy-mm-dd}、{yesterday-yyyy-mm-dd} 换成 2026-10-14 这种。共用的参数写成 {token}，到「宏」里改一次即可。")
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
	if e.code != nil {
		task.Code = e.code.Text()
	}
	task.To = e.to.Text()
	task.Enabled = e.enabled.Checked()
	task.ShowTime = e.showTime.Checked()
	task.ShowName = e.showName.Checked()
	task.Prefix = strings.TrimSpace(e.prefix.Text())
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

func editMacros(owner walk.Form, book *schedule.Book) {
	if book == nil {
		return
	}
	box := &macroBox{book: book, model: &textModel{}}
	var dlg *walk.Dialog
	var list *walk.ListBox
	var detail *walk.Label
	var value *walk.TextEdit
	err := (Dialog{
		AssignTo: &dlg,
		Icon:     appIcon(),
		Title:    "宏",
		Font:     Font{Family: "Microsoft YaHei UI", PointSize: 10},
		MinSize:  Size{Width: 560, Height: 420},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: "任务里写 {名称}。改一个宏，用到它的任务都会换成新值。"},
			ListBox{
				AssignTo:              &list,
				Model:                 box.model,
				MinSize:               Size{Height: 160},
				OnCurrentIndexChanged: func() { box.show() },
			},
			Label{AssignTo: &detail, Text: "还没有宏。"},
			TextEdit{AssignTo: &value, ReadOnly: true, VScroll: true, Text: "点「新建」添加一个，例如名称 token。"},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					PushButton{Text: "新建", OnClicked: func() { box.create() }},
					PushButton{Text: "编辑", OnClicked: func() { box.edit() }},
					PushButton{Text: "删除", OnClicked: func() { box.remove() }},
					HSpacer{},
					PushButton{Text: "关闭", OnClicked: func() { dlg.Accept() }},
				},
			},
		},
	}).Create(owner)
	if err != nil {
		walk.MsgBox(owner, "宏", err.Error(), walk.MsgBoxIconError)
		return
	}
	box.dlg = dlg
	box.list = list
	box.detail = detail
	box.value = value
	box.reload("")
	dlg.Run()
}

type macroBox struct {
	book   *schedule.Book
	dlg    *walk.Dialog
	list   *walk.ListBox
	detail *walk.Label
	value  *walk.TextEdit
	model  *textModel
	items  []schedule.Macro
}

func (b *macroBox) reload(selectName string) {
	items, err := schedule.LoadMacros(b.book.Dir())
	if err != nil {
		b.detail.SetText(err.Error())
		return
	}
	if selectName == "" {
		if item, _, ok := b.selected(); ok {
			selectName = item.Name
		}
	}
	b.items = items
	labels := make([]string, len(items))
	index := -1
	for i, item := range items {
		labels[i] = item.Name
		if item.Name == selectName {
			index = i
		}
	}
	if index < 0 && len(items) > 0 {
		index = 0
	}
	b.model.items = labels
	b.model.PublishItemsReset()
	_ = b.list.SetCurrentIndex(index)
	b.show()
}

func (b *macroBox) show() {
	item, _, ok := b.selected()
	if !ok {
		if len(b.items) == 0 {
			b.detail.SetText("还没有宏。")
			_ = b.value.SetText("点「新建」添加一个，例如名称 token。任务里写 {token}。")
			return
		}
		b.detail.SetText("选一个宏可以查看。")
		_ = b.value.SetText("")
		return
	}
	b.detail.SetText("任务里写 {" + item.Name + "}")
	_ = b.value.SetText(item.Value)
}

func (b *macroBox) selected() (schedule.Macro, int, bool) {
	index := b.list.CurrentIndex()
	if index < 0 || index >= len(b.items) {
		return schedule.Macro{}, -1, false
	}
	return b.items[index], index, true
}

func (b *macroBox) create() {
	var saved string
	if editMacroItem(b.dlg, "新建宏", schedule.Macro{}, func(item schedule.Macro) error {
		saved = strings.TrimSpace(item.Name)
		next := append([]schedule.Macro{}, b.items...)
		next = append(next, item)
		return schedule.SaveMacros(b.book.Dir(), next)
	}) {
		b.reload(saved)
	}
}

func (b *macroBox) edit() {
	current, index, ok := b.selected()
	if !ok {
		b.detail.SetText("先选一个宏。")
		return
	}
	var saved string
	if editMacroItem(b.dlg, "编辑宏", current, func(item schedule.Macro) error {
		saved = strings.TrimSpace(item.Name)
		next := append([]schedule.Macro{}, b.items...)
		next[index] = item
		return schedule.SaveMacros(b.book.Dir(), next)
	}) {
		b.reload(saved)
	}
}

func (b *macroBox) remove() {
	current, index, ok := b.selected()
	if !ok {
		b.detail.SetText("先选一个宏。")
		return
	}
	if walk.MsgBox(b.dlg, "删除宏", "删除「"+current.Name+"」？\n用到 {"+current.Name+"} 的任务不会再换成这个值。", walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != win.IDYES {
		return
	}
	next := append([]schedule.Macro{}, b.items[:index]...)
	next = append(next, b.items[index+1:]...)
	if err := schedule.SaveMacros(b.book.Dir(), next); err != nil {
		b.detail.SetText(err.Error())
		return
	}
	b.reload("")
}

func editMacroItem(owner walk.Form, title string, current schedule.Macro, save func(schedule.Macro) error) bool {
	var dlg *walk.Dialog
	var nameBox *walk.LineEdit
	var valueBox *walk.TextEdit
	var errLabel *walk.Label
	err := (Dialog{
		AssignTo: &dlg,
		Icon:     appIcon(),
		Title:    title,
		Font:     Font{Family: "Microsoft YaHei UI", PointSize: 10},
		MinSize:  Size{Width: 480, Height: 320},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: "名称"},
			LineEdit{AssignTo: &nameBox, Text: current.Name, CueBanner: "例如 token"},
			Label{Text: "值"},
			TextEdit{AssignTo: &valueBox, Text: current.Value, VScroll: true, StretchFactor: 1},
			Label{Text: "任务里写 {名称}。名称保存后不要随便改，不然原来的任务对不上。"},
			Label{AssignTo: &errLabel},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					HSpacer{},
					PushButton{Text: "取消", OnClicked: func() { dlg.Cancel() }},
					PushButton{Text: "保存", OnClicked: func() {
						item := schedule.Macro{Name: nameBox.Text(), Value: valueBox.Text()}
						if err := save(item); err != nil {
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
		walk.MsgBox(owner, "宏", err.Error(), walk.MsgBoxIconError)
		return false
	}
	return dlg.Run() == walk.DlgCmdOK
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
