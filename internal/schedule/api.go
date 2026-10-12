package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const apiTimeout = 20 * time.Second

type apiTarget struct {
	URL     string
	Fields  []string
	Headers []apiHeader
}

type apiHeader struct {
	Name  string
	Value string
}

func parseAPIBody(body string) (apiTarget, error) {
	var target apiTarget
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if header, ok, err := parseAPIHeader(line); ok {
			if err != nil {
				return apiTarget{}, err
			}
			target.Headers = append(target.Headers, header)
			continue
		}
		if target.URL == "" {
			target.URL = line
			continue
		}
		if err := checkAPIField(line); err != nil {
			return apiTarget{}, err
		}
		target.Fields = append(target.Fields, line)
	}
	parsed, err := parsePageURL(target.URL)
	if err != nil {
		return apiTarget{}, errors.New("接口地址要写成 http 或 https 开头的网址")
	}
	target.URL = parsed.String()
	if len(target.Fields) == 0 {
		return apiTarget{}, errors.New("请在网址下面写要发送的字段，例如 data.total")
	}
	return target, nil
}

func parseAPIHeader(line string) (apiHeader, bool, error) {
	const prefix = "header:"
	if len(line) < len(prefix) || !strings.EqualFold(line[:len(prefix)], prefix) {
		return apiHeader{}, false, nil
	}
	rest := strings.TrimSpace(line[len(prefix):])
	name, value, ok := strings.Cut(rest, ":")
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	if !ok || name == "" || value == "" {
		return apiHeader{}, true, errors.New("请求头写成 Header: 名称: 内容")
	}
	if strings.ContainsAny(name, " \r\n") {
		return apiHeader{}, true, errors.New("请求头名称不能有空格")
	}
	return apiHeader{Name: name, Value: value}, true, nil
}

func checkAPIField(path string) error {
	if strings.ContainsAny(path, " \t") {
		return fmt.Errorf("字段 %s 不能有空格", path)
	}
	parts := strings.Split(path, ".")
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("字段 %s 的写法不对", path)
		}
	}
	if utf8.RuneCountInString(path) > 200 {
		return fmt.Errorf("字段名称太长")
	}
	return nil
}

// FetchAPI 请求接口，并取出指定字段。字段用点号逐层写，数组用序号，例如 data.list.0.name。
func FetchAPI(ctx context.Context, body string) (string, error) {
	target, err := parseAPIBody(body)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	payload, err := downloadJSON(ctx, target)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, field := range target.Fields {
		value, ok := lookupJSON(payload, field)
		if !ok {
			lines = append(lines, field+": （没有这个字段）")
			continue
		}
		lines = append(lines, field+": "+formatJSONValue(value))
	}
	return strings.Join(lines, "\n"), nil
}

func downloadJSON(ctx context.Context, target apiTarget) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/json;q=0.9, */*;q=0.1")
	for _, header := range target.Headers {
		req.Header.Set(header.Name, header.Value)
	}
	resp, err := pageClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errPageTimeout
		}
		return nil, errors.New("接口请求失败")
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, maxPageBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, errors.New("接口没有返回完整内容")
	}
	if len(raw) > maxPageBytes {
		return nil, errors.New("接口返回过长")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("接口返回 %d", resp.StatusCode)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var payload any
	if err := dec.Decode(&payload); err != nil {
		return nil, errors.New("接口没有返回 JSON")
	}
	return payload, nil
}

func lookupJSON(node any, path string) (any, bool) {
	cur := node
	for _, part := range strings.Split(path, ".") {
		switch typed := cur.(type) {
		case map[string]any:
			next, ok := typed[part]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			index, err := parseIndex(part, len(typed))
			if err != nil {
				return nil, false
			}
			cur = typed[index]
		default:
			return nil, false
		}
	}
	return cur, true
}

func parseIndex(part string, length int) (int, error) {
	if part == "" || (len(part) > 1 && part[0] == '0') {
		return 0, errors.New("序号不对")
	}
	index := 0
	for _, ch := range part {
		if ch < '0' || ch > '9' {
			return 0, errors.New("序号不对")
		}
		index = index*10 + int(ch-'0')
	}
	if index < 0 || index >= length {
		return 0, errors.New("序号超出范围")
	}
	return index, nil
}

func formatJSONValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "（空）"
	case string:
		return typed
	case json.Number:
		return typed.String()
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(raw)
	}
}
