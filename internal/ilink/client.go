// Package ilink 是微信 ClawBot（iLink Bot API）的最小客户端，只覆盖主动发文本所需的登录、收令牌和发消息。
package ilink

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultBaseURL    = "https://ilinkai.weixin.qq.com"
	ChannelVersion    = "2.4.9"
	AppID             = "bot"
	BotAgent          = "WeixinClawBot/0.1.0 (go)"
	textChunkRunes    = 2000
	maxTextChunks     = 10
	defaultAPIWait    = 15 * time.Second
	defaultQRWait     = 35 * time.Second
	defaultUpdateWait = 40 * time.Second
)

// Client 调用 iLink HTTP 接口。LoginBaseURL 只用于扫码，登录后的业务请求使用账号返回的 baseurl。
type Client struct {
	HTTP           *http.Client
	LoginBaseURL   string
	ChannelVersion string
	AppID          string
	BotAgent       string
}

func NewClient() *Client {
	return &Client{
		HTTP:           &http.Client{},
		LoginBaseURL:   DefaultBaseURL,
		ChannelVersion: ChannelVersion,
		AppID:          AppID,
		BotAgent:       BotAgent,
	}
}

// APIError 是微信业务错误。Code == -14 表示登录失效。
type APIError struct {
	Op      string
	Status  int
	Code    int
	Message string
}

func (e *APIError) Error() string {
	if e.Code == -14 {
		return "微信登录已失效，请重新执行 login"
	}
	if e.Message != "" {
		return fmt.Sprintf("%s失败: %s (%d)", e.Op, e.Message, e.Code)
	}
	if e.Status != 0 {
		return fmt.Sprintf("%s失败: HTTP %d", e.Op, e.Status)
	}
	return fmt.Sprintf("%s失败: %d", e.Op, e.Code)
}

func IsSessionExpired(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == -14
}

type QRCode struct {
	Code       string
	ContentURL string
}

type QRStatus struct {
	Status       string
	BotToken     string
	BotID        string
	BaseURL      string
	UserID       string
	RedirectHost string
}

type Inbound struct {
	FromUserID   string
	ToUserID     string
	MessageType  int
	ContextToken string
	Text         string
}

type Updates struct {
	Messages    []Inbound
	Cursor      string
	PollTimeout time.Duration
}

type Credentials struct {
	BotToken string
	BaseURL  string
	BotID    string
	UserID   string
}

// FetchQR 申请一张登录二维码。localTokens 最多带最近 10 个已保存的 bot token。
func (c *Client) FetchQR(ctx context.Context, localTokens []string) (QRCode, error) {
	if localTokens == nil {
		localTokens = []string{}
	}
	if len(localTokens) > 10 {
		localTokens = localTokens[:10]
	}
	var out struct {
		QRCode           string `json:"qrcode"`
		QRCodeImgContent string `json:"qrcode_img_content"`
	}
	err := c.post(ctx, c.loginBase(), "ilink/bot/get_bot_qrcode?bot_type=3", "", map[string]any{
		"local_token_list": localTokens,
	}, defaultAPIWait, false, &out)
	if err != nil {
		return QRCode{}, err
	}
	if out.QRCode == "" || out.QRCodeImgContent == "" {
		return QRCode{}, &APIError{Op: "获取登录二维码", Message: "响应缺少二维码"}
	}
	return QRCode{Code: out.QRCode, ContentURL: out.QRCodeImgContent}, nil
}

// PollQR 长轮询扫码状态。客户端超时视为继续等待。
func (c *Client) PollQR(ctx context.Context, baseURL, qrcode, verifyCode string) (QRStatus, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = c.loginBase()
	}
	endpoint := "ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qrcode)
	if verifyCode != "" {
		endpoint += "&verify_code=" + url.QueryEscape(verifyCode)
	}
	var out struct {
		Status       string `json:"status"`
		BotToken     string `json:"bot_token"`
		BotID        string `json:"ilink_bot_id"`
		BaseURL      string `json:"baseurl"`
		UserID       string `json:"ilink_user_id"`
		RedirectHost string `json:"redirect_host"`
	}
	err := c.get(ctx, baseURL, endpoint, defaultQRWait, &out)
	if err != nil {
		if isTimeout(err) && ctx.Err() == nil {
			return QRStatus{Status: "wait"}, nil
		}
		return QRStatus{}, err
	}
	return QRStatus{
		Status:       out.Status,
		BotToken:     out.BotToken,
		BotID:        out.BotID,
		BaseURL:      out.BaseURL,
		UserID:       out.UserID,
		RedirectHost: out.RedirectHost,
	}, nil
}

func (c *Client) NotifyStart(ctx context.Context, creds Credentials) error {
	return c.post(ctx, creds.BaseURL, "ilink/bot/msg/notifystart", creds.BotToken, c.baseBody(), defaultAPIWait, true, nil)
}

func (c *Client) NotifyStop(ctx context.Context, creds Credentials) error {
	stopCtx, cancel := context.WithTimeout(context.Background(), defaultAPIWait)
	defer cancel()
	return c.post(stopCtx, creds.BaseURL, "ilink/bot/msg/notifystop", creds.BotToken, c.baseBody(), defaultAPIWait, true, nil)
}

// GetUpdates 长轮询入站消息。调用方用 clientTimeout 略大于服务端挂起时间。
func (c *Client) GetUpdates(ctx context.Context, creds Credentials, cursor string, clientTimeout time.Duration) (Updates, error) {
	if clientTimeout <= 0 {
		clientTimeout = defaultUpdateWait
	}
	var out struct {
		Ret      int    `json:"ret"`
		ErrCode  int    `json:"errcode"`
		ErrMsg   string `json:"errmsg"`
		Cursor   string `json:"get_updates_buf"`
		PollMS   int64  `json:"longpolling_timeout_ms"`
		Messages []struct {
			FromUserID   string    `json:"from_user_id"`
			ToUserID     string    `json:"to_user_id"`
			MessageType  int       `json:"message_type"`
			ContextToken string    `json:"context_token"`
			Text         string    `json:"text"`
			ItemList     []rawItem `json:"item_list"`
		} `json:"msgs"`
	}
	err := c.post(ctx, creds.BaseURL, "ilink/bot/getupdates", creds.BotToken, map[string]any{
		"get_updates_buf": cursor,
		"base_info":       c.baseInfo(),
	}, clientTimeout, true, &out)
	if err != nil {
		if isTimeout(err) && ctx.Err() == nil {
			return Updates{Cursor: cursor}, nil
		}
		return Updates{}, err
	}
	if err := checkAPI("收取消息", out.Ret, out.ErrCode, out.ErrMsg); err != nil {
		return Updates{}, err
	}
	updates := Updates{Cursor: out.Cursor, PollTimeout: time.Duration(out.PollMS) * time.Millisecond}
	if updates.Cursor == "" {
		updates.Cursor = cursor
	}
	for _, msg := range out.Messages {
		text := inboundText(msg.ItemList)
		if text == "" {
			text = strings.TrimSpace(msg.Text)
		}
		updates.Messages = append(updates.Messages, Inbound{
			FromUserID:   msg.FromUserID,
			ToUserID:     msg.ToUserID,
			MessageType:  msg.MessageType,
			ContextToken: msg.ContextToken,
			Text:         text,
		})
	}
	return updates, nil
}

const (
	// RejectExpired 表示微信拒绝了 context_token，通常是令牌已失效。
	RejectExpired = "expired"
	// RejectRateLimited 表示微信拒绝继续发送，通常是主动消息条数用尽。
	RejectRateLimited = "rate_limited"
)

// RejectKind 判断这次发送失败是不是微信明确拒绝了当前会话。
// 参数错误不会记成令牌失效。
func RejectKind(err error) (string, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != -2 {
		return "", false
	}
	msg := apiErr.Message
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "rate") || strings.Contains(msg, "限流") || strings.Contains(msg, "频率") {
		return RejectRateLimited, true
	}
	if strings.Contains(msg, "参数") || strings.Contains(lower, "param") {
		return "", false
	}
	return RejectExpired, true
}

// SendText 把文本发给 toUserID。contextToken 必须来自该用户最近的入站消息。
// 返回值是微信已经接受的条数。
func (c *Client) SendText(ctx context.Context, creds Credentials, toUserID, contextToken, text string) (int, error) {
	parts, err := SplitText(text, textChunkRunes)
	if err != nil {
		return 0, err
	}
	if len(parts) > maxTextChunks {
		return 0, fmt.Errorf("文本过长，拆开后有 %d 条，超过微信主动消息大约 10 条的限额，请缩短内容", len(parts))
	}
	for i, part := range parts {
		body := map[string]any{
			"msg": map[string]any{
				"from_user_id":  "",
				"to_user_id":    toUserID,
				"client_id":     newClientID(),
				"message_type":  2,
				"message_state": 2,
				"context_token": contextToken,
				"item_list": []any{
					map[string]any{
						"type":      1,
						"text_item": map[string]any{"text": part},
					},
				},
			},
			"base_info": c.baseInfo(),
		}
		if err := c.post(ctx, creds.BaseURL, "ilink/bot/sendmessage", creds.BotToken, body, defaultAPIWait, true, nil); err != nil {
			if i > 0 {
				return i, fmt.Errorf("已发出 %d 段，后续失败: %w", i, err)
			}
			return 0, err
		}
	}
	return len(parts), nil
}

// PeerUserID 从一条入站消息里取出微信用户 ID。
func PeerUserID(msg Inbound, botID string) string {
	if isWechatUser(msg.FromUserID) {
		return msg.FromUserID
	}
	if isWechatUser(msg.ToUserID) {
		return msg.ToUserID
	}
	if msg.FromUserID != "" && msg.FromUserID != botID && !strings.HasSuffix(msg.FromUserID, "@im.bot") {
		return msg.FromUserID
	}
	return ""
}

func isWechatUser(id string) bool {
	return strings.HasSuffix(id, "@im.wechat")
}

type rawItem struct {
	Type     int    `json:"type"`
	Text     string `json:"text"`
	TextItem struct {
		Text string `json:"text"`
	} `json:"text_item"`
	ImageItem json.RawMessage `json:"image_item"`
	VoiceItem json.RawMessage `json:"voice_item"`
	VideoItem json.RawMessage `json:"video_item"`
	FileItem  json.RawMessage `json:"file_item"`
}

func inboundText(items []rawItem) string {
	var lines []string
	for _, item := range items {
		if text := strings.TrimSpace(item.TextItem.Text); text != "" {
			lines = append(lines, text)
			continue
		}
		if text := strings.TrimSpace(item.Text); text != "" {
			lines = append(lines, text)
			continue
		}
		switch {
		case hasItem(item.ImageItem) || item.Type == 2:
			lines = append(lines, "［图片］")
		case hasItem(item.VoiceItem) || item.Type == 3:
			lines = append(lines, "［语音］")
		case hasItem(item.VideoItem) || item.Type == 4:
			lines = append(lines, "［视频］")
		case hasItem(item.FileItem) || item.Type == 5:
			lines = append(lines, "［文件］")
		case item.Type != 0 && item.Type != 1:
			lines = append(lines, "［其他消息］")
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func hasItem(raw json.RawMessage) bool {
	text := strings.TrimSpace(string(raw))
	return text != "" && text != "null"
}

// SplitText 按 Unicode 字符切分，优先在换行处切开。
func SplitText(text string, limit int) ([]string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("消息内容不能为空")
	}
	if limit <= 0 {
		limit = textChunkRunes
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}, nil
	}
	var parts []string
	for len(runes) > 0 {
		if len(runes) <= limit {
			parts = append(parts, string(runes))
			break
		}
		cut := limit
		for i := limit; i > limit/2; i-- {
			if runes[i-1] == '\n' {
				cut = i
				break
			}
		}
		parts = append(parts, strings.TrimRight(string(runes[:cut]), "\n"))
		runes = runes[cut:]
		for len(runes) > 0 && runes[0] == '\n' {
			runes = runes[1:]
		}
	}
	out := parts[:0]
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("消息内容不能为空")
	}
	return out, nil
}

func (c *Client) loginBase() string {
	if strings.TrimSpace(c.LoginBaseURL) == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(c.LoginBaseURL, "/")
}

func (c *Client) baseInfo() map[string]string {
	return map[string]string{
		"channel_version": c.version(),
		"bot_agent":       c.agent(),
	}
}

func (c *Client) baseBody() map[string]any {
	return map[string]any{"base_info": c.baseInfo()}
}

func (c *Client) version() string {
	if c.ChannelVersion == "" {
		return ChannelVersion
	}
	return c.ChannelVersion
}

func (c *Client) agent() string {
	if c.BotAgent == "" {
		return BotAgent
	}
	return c.BotAgent
}

func (c *Client) appID() string {
	if c.AppID == "" {
		return AppID
	}
	return c.AppID
}

func (c *Client) post(ctx context.Context, baseURL, endpoint, token string, body any, timeout time.Duration, withBaseCheck bool, dest any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, baseURL, endpoint, token, raw, timeout, true, withBaseCheck, dest)
}

func (c *Client) get(ctx context.Context, baseURL, endpoint string, timeout time.Duration, dest any) error {
	return c.do(ctx, http.MethodGet, baseURL, endpoint, "", nil, timeout, false, false, dest)
}

func (c *Client) do(ctx context.Context, method, baseURL, endpoint, token string, body []byte, timeout time.Duration, authHeaders, checkRet bool, dest any) error {
	if timeout <= 0 {
		timeout = defaultAPIWait
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	endpointURL, err := url.Parse(strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(endpoint, "/"))
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, endpointURL.String(), reader)
	if err != nil {
		return err
	}
	req.Header.Set("iLink-App-Id", c.appID())
	req.Header.Set("iLink-App-ClientVersion", strconv.FormatUint(uint64(ClientVersion(c.version())), 10))
	if authHeaders {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("AuthorizationType", "ilink_bot_token")
		req.Header.Set("X-WECHAT-UIN", randomWechatUIN())
		if strings.TrimSpace(token) != "" {
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
		}
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Op: endpointName(endpoint), Status: resp.StatusCode, Message: trimBody(respBody)}
	}
	if dest != nil && len(bytes.TrimSpace(respBody)) > 0 {
		if err := json.Unmarshal(respBody, dest); err != nil {
			return fmt.Errorf("解析%s响应失败: %w", endpointName(endpoint), err)
		}
	}
	if checkRet {
		var env struct {
			Ret     int    `json:"ret"`
			ErrCode int    `json:"errcode"`
			ErrMsg  string `json:"errmsg"`
		}
		if len(bytes.TrimSpace(respBody)) > 0 {
			if err := json.Unmarshal(respBody, &env); err != nil {
				return fmt.Errorf("解析%s响应失败: %w", endpointName(endpoint), err)
			}
		}
		if err := checkAPI(endpointName(endpoint), env.Ret, env.ErrCode, env.ErrMsg); err != nil {
			return err
		}
	}
	return nil
}

func checkAPI(op string, ret, errcode int, errmsg string) error {
	code := ret
	if code == 0 {
		code = errcode
	}
	if code == 0 {
		return nil
	}
	return &APIError{Op: op, Code: code, Message: errmsg}
}

func endpointName(endpoint string) string {
	path := endpoint
	if i := strings.Index(path, "?"); i >= 0 {
		path = path[:i]
	}
	switch {
	case strings.HasSuffix(path, "sendmessage"):
		return "发送消息"
	case strings.HasSuffix(path, "getupdates"):
		return "收取消息"
	case strings.HasSuffix(path, "get_bot_qrcode"):
		return "获取登录二维码"
	case strings.HasSuffix(path, "get_qrcode_status"):
		return "查询扫码状态"
	default:
		return "请求微信"
	}
}

func trimBody(body []byte) string {
	s := strings.TrimSpace(string(body))
	if !utf8.ValidString(s) {
		s = "<非文本响应>"
	}
	if len(s) > 180 {
		s = s[:180]
	}
	return s
}

// ClientVersion 把 x.y.z 编成官方使用的无符号整数：major<<16 | minor<<8 | patch。
func ClientVersion(version string) uint32 {
	parts := strings.Split(version, ".")
	var nums [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		n, _ := strconv.Atoi(parts[i])
		nums[i] = n
	}
	return uint32((nums[0]&0xff)<<16 | (nums[1]&0xff)<<8 | (nums[2] & 0xff))
}

func randomWechatUIN() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		binary.BigEndian.PutUint32(buf[:], uint32(time.Now().UnixNano()))
	}
	n := binary.BigEndian.Uint32(buf[:])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(n), 10)))
}

func newClientID() string {
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("weixinclawbot-%d-%x", time.Now().UnixMilli(), buf)
}

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
