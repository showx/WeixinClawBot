# WeixinClawBot

用 Go 调用微信官方 iLink ClawBot 接口，主动给个人微信发文本。

对方要先给这个 ClawBot 发过一条消息，程序才能拿到会话令牌。微信不提供令牌过期时间。程序会先尝试发送；微信一旦拒绝，就锁定该会话，直到对方再发一条消息。超过 24 小时或已发约 10 条时，`sessions` 会提示风险，但不会提前拦截。

## 使用

桌面程序：

```bash
go build -ldflags "-H windowsgui" -o WeixinClawBot.exe ./cmd/weixinclawbot
```

双击 `WeixinClawBot.exe` 打开窗口。也可以：

```bash
go run ./cmd/weixinclawbot gui
```

在窗口里扫码添加微信、看每个会话能不能发、直接写消息。窗口开着就会监听已登录的微信，之后新扫码的也会自动开始监听。关掉窗口就停止监听。

窗口里的「定时任务」可以每天固定时间，或每隔若干分钟，把一段文字、一条本机命令的输出、一个网页正文，或接口 JSON 里的指定字段，发到指定微信。抓取页面时填写 http 或 https 网址。普通网页直接读正文；靠脚本画出来的页面，会用本机已经登录的 Edge 或 Chrome 打开，等页面和请求都加载完再读出文字，然后关掉这个新窗口。接口数据的第一行写网址，下面每行写一个字段，例如 `data.total` 或 `data.list.0.name`，需要令牌时加一行 `Header: Authorization: Bearer 令牌`。接收者留空时发给该微信最近的会话。新建的任务不会马上执行。桌面窗口或 `serve` 开着才会到点运行；每天的任务如果当天过了时间还没跑，只补今天这一次。任务保存在数据目录的 `schedules.json`。命令大约最多执行 45 秒，页面大约最多等待 75 秒，接口大约最多等待 20 秒，输出过长会截断后再发送。

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

可选环境变量：`WEIXIN_DATA_DIR`、`WEIXIN_LISTEN`、`WEIXIN_API_TOKEN`。
