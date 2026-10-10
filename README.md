# WeixinClawBot

用 Go 调用微信官方 iLink ClawBot 接口，主动给个人微信发文本。

对方要先给这个 ClawBot 发过一条消息，程序才能拿到会话令牌。微信不提供令牌过期时间。程序会先尝试发送；微信一旦拒绝，就锁定该会话，直到对方再发一条消息。超过 24 小时或已发约 10 条时，`sessions` 会提示风险，但不会提前拦截。

## 使用

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

`serve` 同时监听 `127.0.0.1:8787`。本地令牌启动时打印，也保存在 `data/api_token.txt`。

```bash
curl -X POST http://127.0.0.1:8787/v1/messages \
  -H "Authorization: Bearer <api_token>" \
  -H "Content-Type: application/json" \
  -d "{\"account\":\"工作\",\"text\":\"你好\"}"
```

多个微信时，请求体里的 `account` 必填。`GET /v1/accounts` 查看已登录微信，`GET /v1/sessions` 查看每个微信下的会话状态。接口只绑定本机，不返回 `context_token`。

可选环境变量：`WEIXIN_DATA_DIR`、`WEIXIN_LISTEN`、`WEIXIN_API_TOKEN`。
