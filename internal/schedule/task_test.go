package schedule

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCopyTask(t *testing.T) {
	src := Task{
		ID: "a", Name: "魔光后台", Account: "工作", Accounts: []string{"工作", "生活"},
		Kind: KindAPI, Body: "https://example.com\ndata.sum",
		ShowTime: true, Prefix: "下午汇总", Code: "zaobao",
		Mode: ModeDaily, At: "08:30", Enabled: true,
		LastRun: time.Now(), LastResult: "已发送",
	}
	next := src.Copy()
	if next.ID != "" || next.Code != "" || !next.LastRun.IsZero() || next.LastResult != "" {
		t.Fatalf("副本不应带上运行记录: %+v", next)
	}
	if next.Name != "魔光后台 副本" || next.Body != src.Body || !next.ShowTime || next.Prefix != src.Prefix {
		t.Fatalf("副本内容不对: %+v", next)
	}
	next.Accounts[0] = "改掉"
	if src.Accounts[0] != "工作" {
		t.Fatal("改副本不应改到原来的微信")
	}
	long := Task{Name: strings.Repeat("名", 32)}
	if n := utf8.RuneCountInString(long.Copy().Name); n > 32 || !strings.HasSuffix(long.Copy().Name, "副本") {
		t.Fatalf("长名称副本: %s", long.Copy().Name)
	}
}

func TestDecorateMessage(t *testing.T) {
	now := time.Date(2026, 10, 14, 16, 57, 0, 0, time.Local)
	task := Task{Name: "魔光后台", ShowTime: true, ShowName: true, Prefix: "下午汇总"}
	got := task.Decorate("部门: 肆狼三组  金额: 27465.55", now)
	want := "下午汇总\n时间: 2026-10-14 16:57\n任务: 魔光后台\n部门: 肆狼三组  金额: 27465.55"
	if got != want {
		t.Fatalf("附带内容不对:\n%s", got)
	}
	plain := Task{Name: "早报"}.Decorate("你好", now)
	if plain != "你好" {
		t.Fatalf("没勾选时不应加内容: %s", plain)
	}
}

func TestDailyDue(t *testing.T) {
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.Local)
	task := Task{Enabled: true, Mode: ModeDaily, At: "09:00", LastRun: now.AddDate(0, 0, -1)}
	if !task.Due(now) {
		t.Fatal("昨天跑过、今天时间已过，应该补跑今天这一次")
	}
	task.LastRun = now.Add(-30 * time.Minute)
	if task.Due(now) {
		t.Fatal("今天这一档已经跑过")
	}
	if task.Due(time.Date(2026, 10, 12, 8, 0, 0, 0, time.Local)) {
		t.Fatal("还没到点")
	}
	task.LastRun = time.Time{}
	if !task.Due(now) {
		t.Fatal("从未运行且今天的时间已过，应该跑")
	}
	task.Enabled = false
	if task.Due(now) {
		t.Fatal("停用后不应该跑")
	}
	missed := Task{Enabled: true, Mode: ModeDaily, At: "09:00", LastRun: now.AddDate(0, 0, -5)}
	if !missed.Due(now) {
		t.Fatal("漏了好几天也只看今天这一档")
	}
}

func TestIntervalDue(t *testing.T) {
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.Local)
	task := Task{Enabled: true, Mode: ModeInterval, EveryMinutes: 15, LastRun: now.Add(-10 * time.Minute)}
	if task.Due(now) {
		t.Fatal("间隔还没到")
	}
	task.LastRun = now.Add(-15 * time.Minute)
	if !task.Due(now) {
		t.Fatal("间隔刚好到了")
	}
	task.LastRun = time.Time{}
	if !task.Due(now) {
		t.Fatal("从未运行的间隔任务应该跑")
	}
}

func TestSaveKeepsNewTaskFromFiring(t *testing.T) {
	book := openTestBook(t)
	saved, err := book.Save(Task{
		Name:    "早报",
		Account: "工作",
		To:      "friend@im.wechat",
		Enabled: true,
		Kind:    KindText,
		Body:    "你好",
		Mode:    ModeDaily,
		At:      "8:30",
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || saved.LastRun.IsZero() {
		t.Fatalf("新建任务应该有编号和上次运行时间: %+v", saved)
	}
	if saved.At != "08:30" {
		t.Fatalf("时间应规范成 08:30，得到 %s", saved.At)
	}
	if saved.Due(time.Now()) {
		t.Fatal("刚保存的任务不应该马上执行")
	}

	saved.Body = "改过"
	updated, err := book.Save(saved)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.LastRun.Equal(saved.LastRun) || updated.Body != "改过" {
		t.Fatalf("更新不应改掉上次运行时间: %+v", updated)
	}

	again, err := Open(filepath.Dir(book.path))
	if err != nil {
		t.Fatal(err)
	}
	list, err := again.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Body != "改过" || !list[0].Enabled {
		t.Fatalf("重新打开后内容不对: %+v", list)
	}
	if err := book.Delete(saved.ID); err != nil {
		t.Fatal(err)
	}
	list, err = book.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("删除后还应为空: %+v", list)
	}
}

func TestRunSendsToEveryAccount(t *testing.T) {
	book := openTestBook(t)
	saved, err := book.Save(Task{
		Name: "早报", Account: "工作", Accounts: []string{"工作", "生活"},
		Enabled: true, Kind: KindText, Body: "你好",
		Mode: ModeDaily, At: "08:30",
	})
	if err != nil {
		t.Fatal(err)
	}
	if label := saved.AccountLabel(); label != "工作、生活" {
		t.Fatalf("展示: %s", label)
	}
	var got []string
	result, err := book.RunOne(context.Background(), saved.ID, func(_ context.Context, account, to, text string) error {
		got = append(got, account+"|"+to+"|"+text)
		if account == "生活" {
			return errors.New("拒收")
		}
		return nil
	})
	if err == nil || !strings.Contains(result, "工作 已发送") || !strings.Contains(result, "生活 失败") {
		t.Fatalf("应继续发给其余微信: result=%s err=%v", result, err)
	}
	if len(got) != 2 || got[0] != "工作||你好" || got[1] != "生活||你好" {
		t.Fatalf("发送对象: %#v", got)
	}
}

func TestValidate(t *testing.T) {
	ok := Task{Name: "早报", Account: "工作", Kind: KindText, Body: "你好", Mode: ModeDaily, At: "08:30"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := ok
	bad.At = "25:00"
	if err := bad.Validate(); err == nil {
		t.Fatal("非法时间应该被拒绝")
	}
	bad = ok
	bad.Kind = KindCommand
	bad.Mode = ModeInterval
	bad.EveryMinutes = 0
	if err := bad.Validate(); err == nil {
		t.Fatal("间隔 0 应该被拒绝")
	}
}

func TestTickSendsOnceAndRecordsFailure(t *testing.T) {
	book := openTestBook(t)
	saved, err := book.Save(Task{
		Name: "日报", Account: "工作", Enabled: true,
		Kind: KindText, Body: "结果", Mode: ModeDaily, At: "09:00",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := book.Mark(saved.ID, time.Now().AddDate(0, 0, -2), ""); err != nil {
		t.Fatal(err)
	}
	var calls int
	send := func(ctx context.Context, account, to, text string) error {
		calls++
		if account != "工作" || text != "结果" {
			t.Fatalf("发送参数不对: %s %s %s", account, to, text)
		}
		return errors.New("微信拒绝")
	}
	book.tick(context.Background(), send)
	book.tick(context.Background(), send)
	if calls != 1 {
		t.Fatalf("失败后不应紧接着重试，发送了 %d 次", calls)
	}
	got, ok, err := book.Get(saved.ID)
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if !strings.HasPrefix(got.LastResult, "发送失败") {
		t.Fatalf("应记下发送失败: %s", got.LastResult)
	}
	if got.Due(time.Now()) {
		t.Fatal("记下失败后，今天这一档不应再到期")
	}
}

func TestPausedSkipsWithoutSending(t *testing.T) {
	book := openTestBook(t)
	on, err := book.SchedulerOn()
	if err != nil || !on {
		t.Fatalf("默认应到点执行: %v %v", on, err)
	}
	saved, err := book.Save(Task{
		Name: "日报", Account: "工作", Enabled: true,
		Kind: KindText, Body: "结果", Mode: ModeDaily, At: "00:00",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := book.Mark(saved.ID, time.Now().AddDate(0, 0, -1), ""); err != nil {
		t.Fatal(err)
	}
	if err := book.SetSchedulerOn(false); err != nil {
		t.Fatal(err)
	}
	on, err = book.SchedulerOn()
	if err != nil || on {
		t.Fatalf("关闭后不应再自动执行: %v %v", on, err)
	}
	calls := 0
	send := func(context.Context, string, string, string) error {
		calls++
		return nil
	}
	book.tick(context.Background(), send)
	if calls != 0 {
		t.Fatalf("关闭后不应发送，发送了 %d 次", calls)
	}
	got, ok, err := book.Get(saved.ID)
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if got.Due(time.Now()) || !strings.Contains(got.LastResult, "已暂停") {
		t.Fatalf("暂停应跳过今天这一档: %+v", got)
	}
	if err := book.SetSchedulerOn(true); err != nil {
		t.Fatal(err)
	}
	book.tick(context.Background(), send)
	if calls != 0 {
		t.Fatalf("重新打开后，今天已跳过的任务不应补发，发送了 %d 次", calls)
	}
}

func TestCommandResultAndEmptyOutput(t *testing.T) {
	book := openTestBook(t)
	empty, err := book.Save(Task{
		Name: "空", Account: "工作", Enabled: true,
		Kind: KindCommand, Body: "nothing", Mode: ModeInterval, EveryMinutes: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	book.runCommand = func(context.Context, string) (string, error) {
		return "  ", nil
	}
	var sent int
	send := func(context.Context, string, string, string) error {
		sent++
		return nil
	}
	if _, err := book.RunOne(context.Background(), empty.ID, send); err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatal("没有输出时不应该发送")
	}
	got, _, _ := book.Get(empty.ID)
	if got.LastResult != "命令没有输出，未发送" {
		t.Fatalf("结果不对: %s", got.LastResult)
	}

	failed, err := book.Save(Task{
		Name: "失败", Account: "工作", Enabled: true,
		Kind: KindCommand, Body: "boom", Mode: ModeInterval, EveryMinutes: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	book.runCommand = func(context.Context, string) (string, error) {
		return "", errors.New("boom")
	}
	var text string
	send = func(_ context.Context, _, _, body string) error {
		text = body
		return nil
	}
	if _, err := book.RunOne(context.Background(), failed.ID, send); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "命令失败") {
		t.Fatalf("应把失败说明发给微信: %s", text)
	}
}

func TestRunOneRejectsOverlap(t *testing.T) {
	book := openTestBook(t)
	saved, err := book.Save(Task{
		Name: "慢", Account: "工作", Enabled: true,
		Kind: KindCommand, Body: "slow", Mode: ModeInterval, EveryMinutes: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	block := make(chan struct{})
	var once sync.Once
	book.runCommand = func(context.Context, string) (string, error) {
		once.Do(func() { close(entered) })
		<-block
		return "好", nil
	}
	done := make(chan struct{})
	go func() {
		_, _ = book.RunOne(context.Background(), saved.ID, func(context.Context, string, string, string) error { return nil })
		close(done)
	}()
	<-entered
	_, err = book.RunOne(context.Background(), saved.ID, func(context.Context, string, string, string) error { return nil })
	close(block)
	<-done
	if err == nil || !strings.Contains(err.Error(), "正在运行") {
		t.Fatalf("重叠运行应被拒绝: %v", err)
	}
}

func TestTruncateAndCorruptFile(t *testing.T) {
	text, cut := truncateRunes(strings.Repeat("字", sendRuneLimit+10), sendRuneLimit)
	if !cut || len([]rune(text)) != sendRuneLimit {
		t.Fatalf("截断长度不对: %d %v", len([]rune(text)), cut)
	}
	book := openTestBook(t)
	if err := os.WriteFile(book.path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := book.List(); err == nil {
		t.Fatal("损坏的文件应该报错")
	}
}

func TestRunCommandEcho(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("命令测试只在 Windows 上跑")
	}
	out, err := RunCommand(context.Background(), "echo schedule-ok")
	if err != nil {
		t.Fatal(err)
	}
	if out != "schedule-ok" {
		t.Fatalf("输出不对: %q", out)
	}
}

func openTestBook(t *testing.T) *Book {
	t.Helper()
	book, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return book
}
