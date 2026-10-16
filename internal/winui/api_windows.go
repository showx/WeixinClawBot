//go:build windows

package winui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lxn/walk"

	"weixinclawbot/internal/schedule"
)

func (s *apiStudio) load(stored string) {
	if s == nil {
		return
	}
	form := schedule.DecodeAPI(stored)
	s.syncing = true
	base, enabled, err := schedule.SplitAPIQuery(form.URL)
	if err != nil {
		base = form.URL
	}
	params := append(enabled, schedule.ParseDisabledParams(form.Params)...)
	headers, herr := schedule.ParseHeaderRows(form.Headers)
	if herr != nil {
		headers = nil
	}
	fields, ferr := schedule.ParseFieldRows(form.Fields)
	if ferr != nil {
		fields = nil
	}
	s.params.set(params)
	s.headers.set(headers)
	s.fields.set(fields)
	if s.body != nil {
		_ = s.body.SetText(form.Body)
	}
	if s.method != nil {
		index := 0
		if strings.EqualFold(form.Method, "POST") {
			index = 1
		}
		_ = s.method.SetCurrentIndex(index)
	}
	shown := form.URL
	if base != "" && len(enabled) > 0 {
		if joined, jerr := schedule.JoinAPIQuery(base, params); jerr == nil {
			shown = joined
		}
	}
	if s.url != nil {
		_ = s.url.SetText(shown)
	}
	if s.status != nil {
		switch {
		case herr != nil:
			s.status.SetText(herr.Error())
		case ferr != nil:
			s.status.SetText(ferr.Error())
		default:
			s.status.SetText("还没有发送")
		}
	}
	s.syncing = false
}

type apiStudio struct {
	owner       *walk.Dialog
	method      *walk.ComboBox
	url         *walk.LineEdit
	body        *walk.TextEdit
	status      *walk.Label
	response    *walk.TextEdit
	choices     *walk.ListBox
	choiceModel *textModel
	choicePaths []string
	dir         string
	sendBtn     *walk.PushButton
	params      *kvList
	headers     *kvList
	fields      *kvList
	preface     func() string
	syncing     bool
	sending     bool
}

func (s *apiStudio) onParams() {
	if s.syncing || s.url == nil || s.params == nil {
		return
	}
	base, _, err := schedule.SplitAPIQuery(s.url.Text())
	if err != nil {
		return
	}
	next, err := schedule.JoinAPIQuery(base, s.params.rows())
	if err != nil || next == s.url.Text() {
		return
	}
	s.syncing = true
	_ = s.url.SetText(next)
	s.syncing = false
}

func (s *apiStudio) onURL() {
	if s.syncing || s.url == nil || s.params == nil {
		return
	}
	_, enabled, err := schedule.SplitAPIQuery(s.url.Text())
	if err != nil {
		return
	}
	var disabled []schedule.APIRow
	for _, row := range s.params.rows() {
		if !row.Enabled && strings.TrimSpace(row.Key) != "" {
			disabled = append(disabled, row)
		}
	}
	s.syncing = true
	s.params.set(append(enabled, disabled...))
	s.syncing = false
}

func (s *apiStudio) form(requireFields bool) (string, error) {
	method := "GET"
	if s.method != nil && s.method.CurrentIndex() == 1 {
		method = "POST"
	}
	rawURL := ""
	if s.url != nil {
		rawURL = s.url.Text()
	}
	base, _, err := schedule.SplitAPIQuery(rawURL)
	if err != nil {
		return "", err
	}
	params := s.params.rows()
	full, err := schedule.JoinAPIQuery(base, params)
	if err != nil {
		return "", err
	}
	var disabled []string
	for _, row := range params {
		if !row.Enabled && strings.TrimSpace(row.Key) != "" {
			disabled = append(disabled, strings.TrimSpace(row.Key)+"="+row.Value)
		}
	}
	body := ""
	if s.body != nil {
		body = s.body.Text()
	}
	return schedule.EncodeAPI(schedule.APIForm{
		Method:  method,
		URL:     full,
		Params:  strings.Join(disabled, "\n"),
		Headers: schedule.FormatHeaderRows(s.headers.rows()),
		Body:    body,
		Fields:  schedule.FormatFieldRows(s.fields.rows()),
	}, requireFields)
}

func (s *apiStudio) send() {
	if s.sending || s.owner == nil {
		return
	}
	text, err := s.form(false)
	if err != nil {
		s.status.SetText(err.Error())
		return
	}
	items, err := schedule.LoadMacros(s.dir)
	if err != nil {
		s.status.SetText(err.Error())
		return
	}
	text = schedule.ExpandMacros(text, items)
	s.sending = true
	s.sendBtn.SetEnabled(false)
	s.status.SetText("正在发送…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		preview, err := schedule.PreviewAPI(ctx, text)
		message := ""
		if err != nil {
			message = err.Error()
		}
		s.owner.Synchronize(func() {
			if s.owner.IsDisposed() {
				return
			}
			s.sending = false
			s.sendBtn.SetEnabled(true)
			if err != nil {
				s.status.SetText(headLine(message))
				if preview.Curl != "" {
					_ = s.response.SetText("curl：\n" + preview.Curl + "\n\n" + message)
				}
				return
			}
			s.showPreview(preview)
		})
	}()
}

func (s *apiStudio) showPreview(preview schedule.APIPreview) {
	line := fmt.Sprintf("%d %s · %d 毫秒", preview.Status, http.StatusText(preview.Status), preview.Millis)
	if preview.Vars != "" {
		line += "，" + preview.Vars
	}
	if preview.Status < 200 || preview.Status >= 300 {
		line = "接口返回 " + line
	}
	whole := preview.Message != "" && strings.TrimSpace(preview.Message) == strings.TrimSpace(preview.Raw)
	if whole {
		line += "，没有勾选字段，会把这段 JSON 发给微信"
	} else if n := len(preview.Choices); n > 0 {
		line += fmt.Sprintf("，可在「字段」里加入 %d 个", n)
	}
	s.status.SetText(line)
	text := preview.Raw
	if preview.Curl != "" {
		text = "curl：\n" + preview.Curl
		if preview.Raw != "" {
			text += "\n\n" + preview.Raw
		}
	}
	head := ""
	if s.preface != nil {
		head = s.preface()
	}
	message := preview.Message
	if strings.TrimSpace(message) != "" {
		message = schedule.DecorateMessage(head, message)
	}
	if message != "" && (!whole || strings.TrimSpace(head) != "") {
		if text != "" {
			text += "\n\n"
		}
		text += "会发给微信：\n" + message
	}
	_ = s.response.SetText(text)
	s.choicePaths = splitLines(preview.Fields)
	s.choiceModel.items = append([]string(nil), preview.Choices...)
	s.choiceModel.PublishItemsReset()
}

func (s *apiStudio) addField() {
	if s == nil || s.fields == nil {
		return
	}
	s.fields.addAndShow(schedule.APIRow{Enabled: true})
	if s.status != nil {
		s.status.SetText("已加一行。左边写显示名，右边写路径，例如 data.list")
	}
}

func (s *apiStudio) takeSelected() {
	if s.choices == nil {
		return
	}
	var paths []string
	for _, index := range s.choices.SelectedIndexes() {
		if index >= 0 && index < len(s.choicePaths) {
			paths = append(paths, s.choicePaths[index])
		}
	}
	if len(paths) == 0 {
		s.status.SetText("先在返回结果下面选中要加入的字段")
		return
	}
	s.fields.addPaths(paths)
	s.status.SetText(fmt.Sprintf("已加入 %d 个字段", len(paths)))
}

func (s *apiStudio) takeAll() {
	if len(s.choicePaths) == 0 {
		s.status.SetText("还没有可加入的字段")
		return
	}
	before := len(s.fields.rows())
	s.fields.addPaths(s.choicePaths)
	s.status.SetText(fmt.Sprintf("已加入 %d 个字段", len(s.fields.rows())-before))
}

func splitLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

type kvLine struct {
	on    *walk.CheckBox
	key   *walk.LineEdit
	val   *walk.LineEdit
	box   *walk.Composite
	sized int
}

type kvList struct {
	parent   *walk.Composite
	header   *walk.Composite
	lines    []*kvLine
	keyCue   string
	valCue   string
	onChange func()
	font     *walk.Font
	brush    *walk.SolidColorBrush
	fitting  bool
	width    int
}

func newKVList(parent *walk.Composite, keyCue, valCue string, onChange func()) *kvList {
	list := &kvList{parent: parent, keyCue: keyCue, valCue: valCue, onChange: onChange}
	if parent == nil {
		return list
	}
	if box, ok := parent.Layout().(*walk.BoxLayout); ok {
		_ = box.SetAlignment(walk.AlignHNearVNear)
		_ = box.SetSpacing(4)
	}
	font, err := walk.NewFont("Microsoft YaHei UI", 9, 0)
	if err == nil {
		list.font = font
	}
	brush, err := walk.NewSolidColorBrush(walk.RGB(244, 246, 248))
	if err == nil {
		list.brush = brush
	}
	list.header = list.addHeader()
	parent.SizeChanged().Attach(func() { list.fitWidth() })
	return list
}

func (l *kvList) rows() []schedule.APIRow {
	if l == nil {
		return nil
	}
	var rows []schedule.APIRow
	for _, line := range l.lines {
		rows = append(rows, schedule.APIRow{
			Enabled: line.on.Checked(),
			Key:     strings.TrimSpace(line.key.Text()),
			Value:   line.val.Text(),
		})
	}
	return rows
}

func (l *kvList) set(rows []schedule.APIRow) {
	if l == nil || l.parent == nil {
		return
	}
	prev := l.onChange
	l.onChange = nil
	l.parent.SetSuspended(true)
	for _, line := range l.lines {
		line.box.Dispose()
	}
	l.lines = nil
	if len(rows) == 0 {
		l.add(schedule.APIRow{Enabled: true})
	} else {
		for _, row := range rows {
			l.add(row)
		}
	}
	l.parent.SetSuspended(false)
	l.onChange = prev
	l.ensureTrailing()
	l.fitWidth()
	l.fitHeight()
}

func (l *kvList) addHeader() *walk.Composite {
	if l == nil || l.parent == nil {
		return nil
	}
	box, err := walk.NewComposite(l.parent)
	if err != nil {
		return nil
	}
	layout := walk.NewGridLayout()
	_ = layout.SetMargins(walk.Margins{HNear: 4, VNear: 4, HFar: 4, VFar: 4})
	_ = layout.SetSpacing(8)
	_ = layout.SetAlignment(walk.AlignHNearVCenter)
	if err := box.SetLayout(layout); err != nil {
		box.Dispose()
		return nil
	}
	_ = box.SetAlignment(walk.AlignHNearVNear)
	if l.brush != nil {
		box.SetBackground(l.brush)
	}
	titles := []string{"", l.keyCue, l.valCue, ""}
	for column, title := range titles {
		label, err := walk.NewLabel(box)
		if err != nil {
			box.Dispose()
			return nil
		}
		_ = label.SetText(title)
		label.SetTextColor(walk.RGB(90, 96, 104))
		if l.font != nil {
			label.SetFont(l.font)
		}
		_ = label.SetAlignment(walk.AlignHNearVCenter)
		_ = layout.SetRange(label, walk.Rectangle{X: column, Y: 0, Width: 1, Height: 1})
	}
	_ = layout.SetColumnStretchFactor(0, 1)
	_ = layout.SetColumnStretchFactor(1, 8)
	_ = layout.SetColumnStretchFactor(2, 8)
	_ = layout.SetColumnStretchFactor(3, 2)
	return box
}

func (l *kvList) add(row schedule.APIRow) {
	if l == nil || l.parent == nil {
		return
	}
	box, err := walk.NewComposite(l.parent)
	if err != nil {
		return
	}
	layout := walk.NewGridLayout()
	_ = layout.SetMargins(walk.Margins{HNear: 4, VNear: 2, HFar: 4, VFar: 2})
	_ = layout.SetSpacing(8)
	_ = layout.SetAlignment(walk.AlignHNearVCenter)
	if err := box.SetLayout(layout); err != nil {
		box.Dispose()
		return
	}
	_ = box.SetAlignment(walk.AlignHNearVNear)
	on, err := walk.NewCheckBox(box)
	if err != nil {
		box.Dispose()
		return
	}
	_ = on.SetAlignment(walk.AlignHCenterVCenter)
	on.SetChecked(row.Enabled)
	key, err := walk.NewLineEdit(box)
	if err != nil {
		box.Dispose()
		return
	}
	_ = key.SetCueBanner(l.keyCue)
	_ = key.SetText(row.Key)
	val, err := walk.NewLineEdit(box)
	if err != nil {
		box.Dispose()
		return
	}
	_ = val.SetCueBanner(l.valCue)
	_ = val.SetText(row.Value)
	del, err := walk.NewPushButton(box)
	if err != nil {
		box.Dispose()
		return
	}
	_ = del.SetText("删除")
	_ = del.SetAlignment(walk.AlignHCenterVCenter)
	place := func(widget walk.Widget, column int) {
		_ = layout.SetRange(widget, walk.Rectangle{X: column, Y: 0, Width: 1, Height: 1})
	}
	place(on, 0)
	place(key, 1)
	place(val, 2)
	place(del, 3)
	_ = layout.SetColumnStretchFactor(0, 1)
	_ = layout.SetColumnStretchFactor(1, 8)
	_ = layout.SetColumnStretchFactor(2, 8)
	_ = layout.SetColumnStretchFactor(3, 2)
	line := &kvLine{on: on, key: key, val: val, box: box}
	l.lines = append(l.lines, line)
	del.Clicked().Attach(func() {
		l.parent.Synchronize(func() { l.remove(line) })
	})
	changed := func() {
		l.ensureTrailing()
		if l.onChange != nil {
			l.onChange()
		}
	}
	on.CheckedChanged().Attach(changed)
	key.TextChanged().Attach(changed)
	val.TextChanged().Attach(changed)
	l.fitWidth()
	l.fitHeight()
	if l.onChange != nil {
		l.onChange()
	}
}

func (l *kvList) addAndShow(row schedule.APIRow) {
	if l == nil {
		return
	}
	before := len(l.lines)
	l.add(row)
	if l.parent == nil || len(l.lines) <= before {
		return
	}
	line := l.lines[len(l.lines)-1]
	l.parent.Synchronize(func() {
		if line.box == nil || line.box.IsDisposed() {
			return
		}
		_ = line.val.SetFocus()
	})
}

func (l *kvList) fitHeight() {
	if l == nil || l.parent == nil {
		return
	}
	height := 4
	if l.header != nil {
		height += 28
	}
	height += len(l.lines) * 34
	if height < 36 {
		height = 36
	}
	_ = l.parent.SetMinMaxSize(walk.Size{Height: height}, walk.Size{Height: height})
	l.parent.RequestLayout()
	if form := l.parent.Form(); form != nil {
		form.RequestLayout()
	}
}

func (l *kvList) fitWidth() {
	if l == nil || l.parent == nil || l.fitting {
		return
	}
	width := l.parent.ClientBounds().Width - 8
	if width < 80 {
		return
	}
	l.fitting = true
	if l.header != nil && l.width != width {
		_ = l.header.SetMinMaxSize(walk.Size{Width: width, Height: 24}, walk.Size{})
	}
	for _, line := range l.lines {
		if line.sized == width {
			continue
		}
		_ = line.box.SetMinMaxSize(walk.Size{Width: width, Height: 30}, walk.Size{})
		line.sized = width
	}
	l.width = width
	l.fitting = false
}

func (l *kvList) remove(line *kvLine) {
	next := l.lines[:0]
	for _, item := range l.lines {
		if item != line {
			next = append(next, item)
		}
	}
	l.lines = next
	line.box.Dispose()
	if len(l.lines) == 0 {
		l.add(schedule.APIRow{Enabled: true})
		return
	}
	l.ensureTrailing()
	l.fitHeight()
	if l.onChange != nil {
		l.onChange()
	}
}

func (l *kvList) ensureTrailing() {
	if l == nil {
		return
	}
	if len(l.lines) == 0 {
		l.add(schedule.APIRow{Enabled: true})
		return
	}
	last := l.lines[len(l.lines)-1]
	if strings.TrimSpace(last.key.Text()) == "" && strings.TrimSpace(last.val.Text()) == "" {
		return
	}
	l.add(schedule.APIRow{Enabled: true})
}

func (l *kvList) addPaths(paths []string) {
	seen := map[string]bool{}
	for _, row := range l.rows() {
		if path := strings.TrimSpace(row.Value); path != "" {
			seen[path] = true
		}
	}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			continue
		}
		l.add(schedule.APIRow{Enabled: true, Value: path})
		seen[path] = true
	}
	l.ensureTrailing()
}
