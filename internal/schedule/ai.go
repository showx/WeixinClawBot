package schedule

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const aiFileName = "ai.json"

// AIConfig 是「浏览分析」用的模型接口。Base 写到 /v1，例如 https://api.openai.com/v1。
type AIConfig struct {
	Base  string `json:"base"`
	Key   string `json:"key"`
	Model string `json:"model"`
}

// LoadAI 读取数据目录里的 ai.json。环境变量 WEIXIN_AI_BASE、WEIXIN_AI_KEY、WEIXIN_AI_MODEL 会盖过文件。
func LoadAI(dir string) (AIConfig, error) {
	var cfg AIConfig
	raw, err := os.ReadFile(filepath.Join(dir, aiFileName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return AIConfig{}, err
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return AIConfig{}, errors.New("模型配置文件损坏")
		}
	}
	if v := strings.TrimSpace(os.Getenv("WEIXIN_AI_BASE")); v != "" {
		cfg.Base = v
	}
	if v := strings.TrimSpace(os.Getenv("WEIXIN_AI_KEY")); v != "" {
		cfg.Key = v
	}
	if v := strings.TrimSpace(os.Getenv("WEIXIN_AI_MODEL")); v != "" {
		cfg.Model = v
	}
	cfg.Base = strings.TrimSpace(cfg.Base)
	cfg.Key = strings.TrimSpace(cfg.Key)
	cfg.Model = strings.TrimSpace(cfg.Model)
	return cfg, nil
}

// SaveAI 把模型接口写到数据目录的 ai.json。
func SaveAI(dir string, cfg AIConfig) error {
	base, err := normalizeAIBase(cfg.Base)
	if err != nil {
		return err
	}
	cfg.Base = base
	cfg.Key = strings.TrimSpace(cfg.Key)
	cfg.Model = strings.TrimSpace(cfg.Model)
	if cfg.Key == "" {
		return errors.New("请填写密钥")
	}
	if utf8.RuneCountInString(cfg.Key) > 2000 {
		return errors.New("密钥太长")
	}
	if cfg.Model == "" {
		return errors.New("请填写模型名")
	}
	if utf8.RuneCountInString(cfg.Model) > 80 {
		return errors.New("模型名太长")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writePrivate(filepath.Join(dir, aiFileName), raw)
}

// Ready 检查接口地址、密钥和模型名是否都能用。
func (c AIConfig) Ready() error {
	if strings.TrimSpace(c.Base) == "" || strings.TrimSpace(c.Key) == "" || strings.TrimSpace(c.Model) == "" {
		return errors.New("还没有配置模型。在定时任务里打开「模型」填写接口地址、密钥和模型名")
	}
	if _, err := normalizeAIBase(c.Base); err != nil {
		return err
	}
	return nil
}

// Complete 调用兼容 OpenAI 的对话接口，返回模型给出的文字。
func (c AIConfig) Complete(ctx context.Context, system, user string) (string, error) {
	if err := c.Ready(); err != nil {
		return "", err
	}
	base, err := normalizeAIBase(c.Base)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"model":       c.Model,
		"temperature": 0,
		"max_tokens":  800,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	resp, err := aiClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("模型没有连上")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", errors.New("模型没有返回完整内容")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", modelError(resp.StatusCode, body, c.Key)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Choices) == 0 {
		return "", errors.New("模型没有返回内容")
	}
	text, err := chatContent(parsed.Choices[0].Message.Content)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("模型没有返回内容")
	}
	return text, nil
}

var aiClient = &http.Client{}

func normalizeAIBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, "/")
	lower := strings.ToLower(raw)
	const suffix = "/chat/completions"
	if strings.HasSuffix(lower, suffix) {
		raw = raw[:len(raw)-len(suffix)]
		raw = strings.TrimRight(raw, "/")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("模型接口要写成 http 或 https 开头的地址")
	}
	if parsed.User != nil {
		return "", errors.New("模型接口不要把密钥写在地址里")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func modelError(status int, body []byte, key string) error {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := ""
	if json.Unmarshal(body, &parsed) == nil {
		msg = strings.TrimSpace(parsed.Error.Message)
	}
	if key != "" && msg != "" && strings.Contains(msg, key) {
		msg = ""
	}
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg, _ = truncateRunes(msg, 80)
	if msg == "" {
		return errors.New("模型接口返回 " + strconv.Itoa(status))
	}
	return errors.New("模型接口返回 " + strconv.Itoa(status) + "：" + msg)
}

func chatContent(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", errors.New("模型没有返回内容")
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", errors.New("模型没有返回内容")
		}
		return text, nil
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, part := range parts {
			b.WriteString(part.Text)
		}
		if strings.TrimSpace(b.String()) != "" {
			return b.String(), nil
		}
	}
	return "", errors.New("模型没有返回内容")
}

func writePrivate(path string, raw []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err == nil {
		return nil
	}
	_ = os.Remove(path)
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
