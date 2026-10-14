package schedule

import (
	"fmt"
	"net/url"
	"strings"
)

// APIRow 是参数、请求头或字段里的一行。字段的 Key 是显示名，Value 是路径。
type APIRow struct {
	Enabled bool
	Key     string
	Value   string
}

// SplitAPIQuery 把网址拆成不带查询串的地址，以及查询参数。
func SplitAPIQuery(raw string) (string, []APIRow, error) {
	parsed, err := parsePageURL(raw)
	if err != nil {
		return "", nil, err
	}
	query := parsed.RawQuery
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	base := parsed.String()
	if query == "" {
		return base, nil, nil
	}
	var rows []APIRow
	for _, part := range strings.Split(query, "&") {
		if part == "" {
			continue
		}
		key, val, _ := strings.Cut(part, "=")
		key, err1 := url.QueryUnescape(key)
		val, err2 := url.QueryUnescape(val)
		if err1 != nil || err2 != nil {
			return "", nil, fmt.Errorf("参数里有无法识别的内容")
		}
		if key == "" {
			continue
		}
		rows = append(rows, APIRow{Enabled: true, Key: key, Value: val})
	}
	return base, rows, nil
}

// JoinAPIQuery 用勾选的参数重写查询串。没勾选的参数不会出现在网址里。
func JoinAPIQuery(base string, rows []APIRow) (string, error) {
	parsed, err := parsePageURL(base)
	if err != nil {
		return "", err
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	var parts []string
	for _, row := range rows {
		if !row.Enabled || strings.TrimSpace(row.Key) == "" {
			continue
		}
		parts = append(parts, url.QueryEscape(strings.TrimSpace(row.Key))+"="+url.QueryEscape(row.Value))
	}
	parsed.RawQuery = strings.Join(parts, "&")
	return parsed.String(), nil
}

// ParseHeaderRows 读取请求头。以 // 开头的一行是取消勾选的。
func ParseHeaderRows(text string) ([]APIRow, error) {
	var rows []APIRow
	for _, line := range splitPlainLines(text) {
		enabled := true
		if rest, ok := strings.CutPrefix(line, "//"); ok {
			enabled = false
			line = strings.TrimSpace(rest)
		}
		if line == "" {
			continue
		}
		if _, ok := trimPrefixFold(line, "header:"); !ok {
			line = "Header: " + line
		}
		header, ok, err := parseAPIHeader(line)
		if err != nil || !ok {
			if !enabled {
				continue
			}
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("请求头写成 名称: 内容，例如 Authorization: Bearer 令牌")
		}
		rows = append(rows, APIRow{Enabled: enabled, Key: header.Name, Value: header.Value})
	}
	return rows, nil
}

// FormatHeaderRows 把请求头写回文本。
func FormatHeaderRows(rows []APIRow) string {
	var lines []string
	for _, row := range rows {
		if strings.TrimSpace(row.Key) == "" {
			continue
		}
		line := row.Key + ": " + row.Value
		if !row.Enabled {
			line = "// " + line
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// ParseFieldRows 读取要发送的字段。Key 是显示名，Value 是路径。
func ParseFieldRows(text string) ([]APIRow, error) {
	var rows []APIRow
	for _, line := range splitPlainLines(text) {
		enabled := true
		if rest, ok := strings.CutPrefix(line, "//"); ok {
			enabled = false
			line = strings.TrimSpace(rest)
		}
		if rest, ok := trimPrefixFold(line, "field:"); ok {
			line = strings.TrimSpace(rest)
		}
		if line == "" {
			continue
		}
		field, err := parseAPIFieldLine(line)
		if err != nil {
			if !enabled {
				continue
			}
			return nil, err
		}
		rows = append(rows, APIRow{Enabled: enabled, Key: field.Label, Value: field.Path})
	}
	return rows, nil
}

// FormatFieldRows 把字段写回文本。
func FormatFieldRows(rows []APIRow) string {
	var lines []string
	for _, row := range rows {
		path := strings.TrimSpace(row.Value)
		if path == "" {
			continue
		}
		line := path
		label := strings.TrimSpace(row.Key)
		if label != "" && label != path {
			line = label + " = " + path
		}
		if !row.Enabled {
			line = "// " + line
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// ParseDisabledParams 读取没有勾选、因此不会出现在网址里的参数。
func ParseDisabledParams(text string) []APIRow {
	var rows []APIRow
	for _, line := range splitPlainLines(text) {
		key, val, _ := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		rows = append(rows, APIRow{Enabled: false, Key: key, Value: strings.TrimSpace(val)})
	}
	return rows
}

// APISummary 用一句话说明这条接口请求。
func APISummary(body string) string {
	form := DecodeAPI(body)
	if strings.TrimSpace(form.URL) == "" {
		return "还没有填写请求"
	}
	n := 0
	rows, err := ParseFieldRows(form.Fields)
	if err == nil {
		for _, row := range rows {
			if row.Enabled && strings.TrimSpace(row.Value) != "" {
				n++
			}
		}
	}
	method := form.Method
	if method == "" {
		method = "GET"
	}
	return fmt.Sprintf("%s %s · %d 个字段", method, form.URL, n)
}
