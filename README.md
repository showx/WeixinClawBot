# WeixinClawBot

用 Go 调用微信官方 iLink ClawBot 接口，主动给个人微信发文本。

对方要先给这个 ClawBot 发过一条消息，程序才能拿到会话令牌。这个令牌和扫码登录不是一回事，微信也不提供过期时间。每次发送都会拿上次的令牌再请求微信；微信拒绝后，会话会标成已拒绝，但下次定时任务仍会再试。只有微信继续拒绝时，才需要对方再发一条消息。窗口或 `serve` 开着时，这条新消息会换成新令牌。超过 24 小时或已发约 10 条时，`sessions` 会提示风险，但不会提前拦截。失败原因写在主窗口下方的发送日志里，包括令牌是什么时候拿到的、此后发过几条，以及微信返回的错误码。

## 使用

桌面程序：

```bash
go build -ldflags "-H windowsgui" -o WeixinClawBot.exe ./cmd/weixinclawbot
```

双击 `WeixinClawBot.exe` 打开窗口。也可以：

```bash
go run ./cmd/weixinclawbot gui
```

在窗口里扫码添加微信、看每个会话能不能发、直接写消息。按住 Ctrl 可以多选微信，点「发送」时每个选中的微信都会发同一条内容；接收者留空则各自发给最近的会话。窗口开着就会监听已登录的微信，之后新扫码的也会自动开始监听。关掉窗口就停止监听。

窗口里的「定时任务」可以每天固定时间，或每隔若干分钟，把一段文字、一条本机命令的输出、一个网页正文、接口 JSON 里的指定字段，或一次浏览分析的结论，发到指定微信。抓取页面时填写 http 或 https 网址。普通网页直接读正文；靠脚本画出来的页面，会用本机已经登录的 Edge 或 Chrome 打开，等页面和请求都加载完再读出文字，然后关掉这个新窗口。接口数据选中后，方法和地址在同一行，旁边是「发送」。参数、请求头、正文、字段分成四栏，返回显示在下面。没有勾选字段时，会把返回的 JSON 直接发给微信；勾选后只发这些路径。地址、参数、请求头和正文里可以写日期变量。`{today}`、`{yesterday}` 换成 `20261014`、`20261013`；`{today-yyyy-mm-dd}`、`{yesterday-yyyy-mm-dd}` 换成 `2026-10-14`、`2026-10-13`。列表路径写成 `data.list.*.name`。需要提交内容时把方法改成 POST。浏览分析的第一行写网址，下面写要回答的问题。到点会在后台打开页面，把看到的内容交给模型；模型只按这个问题点击、输入或滚动，然后把结论发出去。页面上的文字不会被当成新的指示。需要登录时可以加一行 `Cookie: 名称=值`；如果本机 Chrome 或 Edge 里这个网站已经登录，会把登录状态复制到临时窗口里操作，做完就关掉，不会去点正在使用的那个窗口。模型接口在定时任务窗口的「模型」里填写，保存在数据目录的 `ai.json`。一个任务可以按住 Ctrl 选择多个微信，到点会把同一条内容分别发出去。接收者留空时，每个微信发给自己最近的会话。新建的任务不会马上执行。桌面窗口或 `serve` 开着才会到点运行。主窗口和定时任务窗口里的「到点执行」可以整组关掉；关掉后仍会监听微信，到点不会发，这段时间错过的任务也不会在重新打开后补发。「现在运行」不受这个开关影响。每天的任务如果当天过了时间还没跑，只补今天这一次。任务保存在数据目录的 `schedules.json`。每次运行、主窗口的即时发送，以及对方从手机发给 ClawBot 的内容，都会记在 `task-log.json`。主窗口下方可以直接看，会话旁边也会显示最近一条手机消息；定时任务窗口的「日志」打开的是同一份记录，最多保留最近 200 条。命令大约最多执行 45 秒，页面大约最多等待 75 秒，接口大约最多等待 20 秒，浏览分析大约最多 3 分钟，输出过长会截断后再发送。

命令行仍然可用：

```bash
go run ./cmd/weixinclawbot login -name 工作
go run ./cmd/weixinclawbot login -name 生活
go run ./cmd/weixinclawbot list
go run ./cmd/weixinclawbot serve
```

`list` 在终端里列出全部微信和各自的会话。`↑↓` 选微信，`←→` 选会话，`Enter` 写一条测试消息并发送，`r` 刷新，`q` 退出。

每个微信单独扫一次码。`login` 会在命令行里画出二维码，并用画图打开图片。扫码确认后，用该微信给 ClawBot 发一条任意消息。`serve` 会同时监听全部已登录微信。

另开一个终端发送。只有一个微信时可以不写 `-account`：

```bash
go run ./cmd/weixinclawbot send -account 工作 你好
go run ./cmd/weixinclawbot send -account 工作 -to 用户ID 你好
go run ./cmd/weixinclawbot sessions
```

`serve` 和桌面窗口都在 `127.0.0.1:8787` 提供发消息接口。本地令牌启动时打印，也保存在 `data/api_token.txt`，窗口里可以复制。

```bash
curl -X POST http://127.0.0.1:8787/v1/messages \
  -H "Authorization: Bearer <api_token>" \
  -H "Content-Type: application/json" \
  -d "{\"account\":\"工作\",\"text\":\"你好\"}"
```

多个微信时，请求体里的 `account` 必填。`GET /v1/accounts` 查看已登录微信，`GET /v1/sessions` 查看每个微信下的会话状态。接口只绑定本机，不返回 `context_token`。

可选环境变量：`WEIXIN_DATA_DIR`、`WEIXIN_LISTEN`、`WEIXIN_API_TOKEN`。模型接口也可以用 `WEIXIN_AI_BASE`、`WEIXIN_AI_KEY`、`WEIXIN_AI_MODEL`，这三项会盖过 `ai.json`。
