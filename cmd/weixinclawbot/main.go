package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"weixinclawbot/internal/app"
	"weixinclawbot/internal/ilink"
	"weixinclawbot/internal/schedule"
	"weixinclawbot/internal/store"
	"weixinclawbot/internal/tui"
	"weixinclawbot/internal/winui"
)

func main() {
	log.SetFlags(log.LstdFlags)
	var err error
	if len(os.Args) < 2 {
		if noConsole() {
			err = cmdGUI(nil)
			if err != nil {
				os.Exit(1)
			}
			return
		}
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "login":
		err = cmdLogin(os.Args[2:])
	case "accounts":
		err = cmdAccounts(os.Args[2:])
	case "list", "tui":
		err = cmdList(os.Args[2:])
	case "gui":
		err = cmdGUI(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	case "send":
		err = cmdSend(os.Args[2:])
	case "sessions":
		err = cmdSessions(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `用法:
  weixinclawbot gui                             打开桌面窗口，并监听全部已登录微信
  weixinclawbot login [-name 名称]              在命令行扫码登录一个微信
  weixinclawbot list                            在终端里查看全部微信和会话
  weixinclawbot serve [-account 名称]           监听微信，并开放发消息接口
  weixinclawbot send [-account 名称] [-to 用户] 文本
  weixinclawbot sessions [-account 名称]        查看可以发送的会话

多个微信时，send 和只看某一个会话都要带 -account。名称示例：-name 工作
状态目录默认是 ./data，可用环境变量 WEIXIN_DATA_DIR 或参数 -data 修改。
gui 和 serve 开着时会执行定时任务，并把结果发到指定微信。
`)
}

func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	dir := dataDirFlag(fs)
	name := fs.String("name", "", "这个微信的本地名称，例如 工作")
	_ = fs.Parse(args)
	application, err := openApp(*dir)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return application.Login(ctx, *name, os.Stdin, os.Stdout)
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	dir := dataDirFlag(fs)
	_ = fs.Parse(args)
	application, err := openApp(*dir)
	if err != nil {
		return err
	}
	return tui.Run(func() ([]tui.Account, error) {
		return loadTUI(application)
	}, func(account, to, text string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := application.SendAccount(ctx, account, to, text)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("已由微信 %s 发送给 %s（本次 %d 条，累计 %d 条）", result.Account, result.To, result.Parts, result.SentSince), nil
	})
}

func loadTUI(application *app.App) ([]tui.Account, error) {
	grouped, err := application.AccountSessions("")
	if err != nil {
		return nil, err
	}
	ready, err := application.Catalog.LoggedIn()
	if err != nil {
		return nil, err
	}
	online := map[string]bool{}
	for _, item := range ready {
		online[item.Account.Name] = true
	}
	now := time.Now()
	out := make([]tui.Account, 0, len(grouped))
	for _, group := range grouped {
		account := tui.Account{
			Name:   group.Account.Name,
			Online: online[group.Account.Name],
			Owner:  group.Account.UserID,
		}
		for _, item := range group.Sessions {
			view := app.DescribeSession(item, now)
			account.Sessions = append(account.Sessions, tui.Session{
				User:    view.UserID,
				Detail:  view.Detail,
				Updated: item.UpdatedAt,
			})
		}
		out = append(out, account)
	}
	return out, nil
}

func cmdAccounts(args []string) error {
	fs := flag.NewFlagSet("accounts", flag.ExitOnError)
	dir := dataDirFlag(fs)
	_ = fs.Parse(args)
	application, err := openApp(*dir)
	if err != nil {
		return err
	}
	list, err := application.Catalog.List()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("还没有登录微信。执行 login -name 名称 添加。")
		return nil
	}
	ready, err := application.Catalog.LoggedIn()
	if err != nil {
		return err
	}
	online := map[string]bool{}
	for _, item := range ready {
		online[item.Account.Name] = true
	}
	for _, item := range list {
		state := "登录已失效，请重新 login -name " + item.Name
		if online[item.Name] {
			state = "已登录"
		}
		fmt.Printf("%s\t%s\t扫码用户 %s\n", item.Name, state, item.UserID)
	}
	return nil
}

func cmdGUI(args []string) error {
	fs := flag.NewFlagSet("gui", flag.ExitOnError)
	dir := dataDirFlag(fs)
	listen := fs.String("listen", envOr("WEIXIN_LISTEN", "127.0.0.1:8787"), "本机发消息接口地址")
	_ = fs.Parse(args)
	application, err := openApp(*dir)
	if err != nil {
		return err
	}
	token, err := apiToken(application)
	if err != nil {
		return err
	}
	return winui.Run(application, *listen, token)
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dir := dataDirFlag(fs)
	listen := fs.String("listen", envOr("WEIXIN_LISTEN", "127.0.0.1:8787"), "本机发消息接口地址")
	account := fs.String("account", "", "只监听这个微信；省略则监听全部已登录微信")
	_ = fs.Parse(args)
	application, err := openApp(*dir)
	if err != nil {
		return err
	}
	if _, err := application.ReadyAccounts(*account); err != nil {
		return err
	}
	token, err := apiToken(application)
	if err != nil {
		return err
	}
	watch := application.Supervise
	if strings.TrimSpace(*account) != "" {
		name := *account
		watch = func(ctx context.Context) error {
			return application.WatchAccounts(ctx, name)
		}
	}
	return serveLoop(application, *listen, token, watch)
}

func serveLoop(application *app.App, listen, token string, watch func(context.Context) error) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	page := browserURL(ln.Addr().String())
	srv := &http.Server{
		Handler:           (&app.Server{App: application, Token: token, Listen: strings.TrimSuffix(strings.TrimPrefix(page, "http://"), "/")}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("发消息接口: POST %sv1/messages", page)
	log.Printf("本地接口令牌: %s", token)
	if application.Catalog != nil {
		book, err := schedule.Open(application.Catalog.Dir())
		if err != nil {
			log.Printf("定时任务未启动: %v", err)
		} else {
			application.SetOnIncoming(func(msg app.Incoming) {
				book.AddPhone(msg.Account, msg.UserID, msg.Text)
				go answerIncoming(application, book, msg, nil)
			})
			go book.Loop(ctx, func(ctx context.Context, account, to, text string) error {
				_, err := application.SendAccount(ctx, account, to, text)
				return err
			})
		}
	}

	listenErr := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			listenErr <- nil
			return
		}
		listenErr <- err
	}()
	watchErr := make(chan error, 1)
	go func() {
		watchErr <- watch(ctx)
	}()

	select {
	case <-ctx.Done():
		shutdown(srv)
		return <-watchErr
	case err := <-watchErr:
		stop()
		shutdown(srv)
		return err
	case err := <-listenErr:
		stop()
		shutdown(srv)
		<-watchErr
		return err
	}
}

func apiToken(application *app.App) (string, error) {
	if token := strings.TrimSpace(os.Getenv("WEIXIN_API_TOKEN")); token != "" {
		return token, nil
	}
	return application.Catalog.APIToken()
}

func browserURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + strings.TrimRight(addr, "/") + "/"
	}
	switch host {
	case "", "::", "0.0.0.0", "[::]":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	dir := dataDirFlag(fs)
	account := fs.String("account", "", "用哪个微信发送；多个微信时必填")
	to := fs.String("to", "", "接收者用户 ID，省略则发给该微信最近发过消息的用户")
	_ = fs.Parse(args)
	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	application, err := openApp(*dir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := application.SendAccount(ctx, *account, *to, text)
	if err != nil {
		return err
	}
	fmt.Printf("已由微信 %s 发送给 %s（本次 %d 条，自上次收到消息后累计 %d 条）\n", result.Account, result.To, result.Parts, result.SentSince)
	return nil
}

func cmdSessions(args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ExitOnError)
	dir := dataDirFlag(fs)
	account := fs.String("account", "", "只看这个微信；省略则列出全部")
	_ = fs.Parse(args)
	application, err := openApp(*dir)
	if err != nil {
		return err
	}
	grouped, err := application.AccountSessions(*account)
	if err != nil {
		return err
	}
	if len(grouped) == 0 {
		fmt.Println("还没有登录微信。先执行 login -name 名称。")
		return nil
	}
	now := time.Now()
	for _, group := range grouped {
		fmt.Printf("微信 %s\n", group.Account.Name)
		if len(group.Sessions) == 0 {
			fmt.Println("  还没有会话。先执行 serve，再让对方给这个 ClawBot 发一条消息。")
			continue
		}
		for _, item := range group.Sessions {
			view := app.DescribeSession(item, now)
			fmt.Printf("  %s\n    %s\n    上次收到消息：%s\n", view.UserID, view.Detail, view.UpdatedAt.Format("2006-01-02 15:04:05"))
		}
	}
	return nil
}

func answerIncoming(application *app.App, book *schedule.Book, msg app.Incoming, done func(string, error)) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	reply, ok, err := book.Answer(ctx, msg.Text)
	if err != nil {
		log.Printf("查询定时任务失败: %v", err)
		if strings.TrimSpace(reply) == "" {
			reply = "运行失败：" + strings.TrimSpace(err.Error())
			ok = true
		}
	}
	if ok {
		if strings.TrimSpace(reply) == "" {
			reply = "这次没有内容"
		}
		application.ReplyIncoming(msg, reply, done)
		return
	}
	application.AckIncoming(msg, done)
}

func openApp(dir string) (*app.App, error) {
	catalog, err := store.OpenCatalog(dir)
	if err != nil {
		return nil, err
	}
	return &app.App{Catalog: catalog, Client: ilink.NewClient(), Log: log.Default()}, nil
}

func dataDirFlag(fs *flag.FlagSet) *string {
	return fs.String("data", envOr("WEIXIN_DATA_DIR", "data"), "状态目录")
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func shutdown(srv *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
