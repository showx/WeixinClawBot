package schedule

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	apiTimeout   = 20 * time.Second
	maxAPIItems  = 20
	maxAPIFields = 40
)

type apiTarget struct {
	Method     string
	URL        string
	Fields     []apiField
	Headers    []apiHeader
	Body       string
	OffParams  []APIRow
	OffHeaders []apiHeader
	OffFields  []apiField
}

type apiField struct {
	Label string
	Path  string
}

type apiHeader struct {
	Name  string
	Value string
}

// APIForm 是接口任务在窗口里分开填写的内容。
type APIForm struct {
	Method  string
	URL     string
	Params  string
	Headers string
	Body    string
	Fields  string
}

// APIPreview 是试取一次接口后能看到的结果。
type APIPreview struct {
	Message string
	Fields  string
	Choices []string
	Detail  string
	Status  int
	Millis  int64
	Raw     string
	Vars    string
}

func parseAPIBody(body string) (apiTarget, error) {
	return parseAPITarget(body, false)
}

func parseAPITarget(body string, requireFields bool) (apiTarget, error) {
	var target apiTarget
	target.Method = http.MethodGet
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "//"); ok {
			if err := target.addDisabled(strings.TrimSpace(rest)); err != nil {
				return apiTarget{}, err
			}
			continue
		}
		if method, ok, err := parseAPIMethod(line); ok {
			if err != nil {
				return apiTarget{}, err
			}
			if target.URL != "" && target.Method != method {
				return apiTarget{}, errors.New("请求方法只能写一次")
			}
			target.Method = method
			continue
		}
		if header, ok, err := parseAPIHeader(line); ok {
			if err != nil {
				return apiTarget{}, err
			}
			target.Headers = append(target.Headers, header)
			continue
		}
		if rest, ok := trimPrefixFold(line, "body:"); ok {
			if target.Body != "" {
				return apiTarget{}, errors.New("请求内容只能写一份")
			}
			target.Body = strings.TrimSpace(rest)
			continue
		}
		if strings.EqualFold(line, "body<<") {
			if target.Body != "" {
				return apiTarget{}, errors.New("请求内容只能写一份")
			}
			var chunk []string
			i++
			for i < len(lines) && !strings.EqualFold(strings.TrimSpace(lines[i]), "body>>") {
				chunk = append(chunk, strings.TrimRight(lines[i], " \t"))
				i++
			}
			if i >= len(lines) {
				return apiTarget{}, errors.New("请求内容没有结束，请补上 Body>>")
			}
			target.Body = strings.TrimSpace(strings.Join(chunk, "\n"))
			continue
		}
		if target.URL == "" {
			if method, rest, ok := splitMethodLine(line); ok {
				target.Method = method
				line = rest
			}
			parsed, err := parsePageURL(line)
			if err != nil {
				return apiTarget{}, errors.New("接口地址要写成 http 或 https 开头的网址")
			}
			target.URL = parsed.String()
			continue
		}
		field, err := parseAPIFieldLine(line)
		if err != nil {
			return apiTarget{}, err
		}
		target.Fields = append(target.Fields, field)
	}
	if target.URL == "" {
		return apiTarget{}, errors.New("接口地址要写成 http 或 https 开头的网址")
	}
	if requireFields && len(target.Fields) == 0 {
		return apiTarget{}, errors.New("请填写要发送的字段，例如 data.total")
	}
	return target, nil
}

func (t *apiTarget) addDisabled(line string) error {
	if line == "" {
		return nil
	}
	if rest, ok := trimPrefixFold(line, "param:"); ok {
		key, val, _ := strings.Cut(strings.TrimSpace(rest), "=")
		key = strings.TrimSpace(key)
		if key == "" {
			return nil
		}
		t.OffParams = append(t.OffParams, APIRow{Key: key, Value: strings.TrimSpace(val)})
		return nil
	}
	if rest, ok := trimPrefixFold(line, "header:"); ok {
		header, ok, err := parseAPIHeader("Header: " + strings.TrimSpace(rest))
		if err != nil || !ok {
			return nil
		}
		t.OffHeaders = append(t.OffHeaders, header)
		return nil
	}
	if rest, ok := trimPrefixFold(line, "field:"); ok {
		line = strings.TrimSpace(rest)
	}
	field, err := parseAPIFieldLine(line)
	if err != nil {
		return nil
	}
	t.OffFields = append(t.OffFields, field)
	return nil
}

func parseAPIMethod(line string) (string, bool, error) {
	rest, ok := trimPrefixFold(line, "method:")
	if !ok {
		return "", false, nil
	}
	method := strings.ToUpper(strings.TrimSpace(rest))
	if method != http.MethodGet && method != http.MethodPost {
		return "", true, errors.New("请求方法只能是 GET 或 POST")
	}
	return method, true, nil
}

func splitMethodLine(line string) (string, string, bool) {
	method, rest, ok := strings.Cut(line, " ")
	if !ok {
		return "", "", false
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	rest = strings.TrimSpace(rest)
	if (method != http.MethodGet && method != http.MethodPost) || !strings.Contains(rest, "://") {
		return "", "", false
	}
	return method, rest, true
}

func parseAPIHeader(line string) (apiHeader, bool, error) {
	rest, ok := trimPrefixFold(line, "header:")
	if !ok {
		return apiHeader{}, false, nil
	}
	name, value, cut := strings.Cut(strings.TrimSpace(rest), ":")
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	if !cut || name == "" || value == "" {
		return apiHeader{}, true, errors.New("请求头写成 名称: 内容，例如 Authorization: Bearer 令牌")
	}
	if strings.ContainsAny(name, " \r\n") {
		return apiHeader{}, true, errors.New("请求头名称不能有空格")
	}
	return apiHeader{Name: name, Value: value}, true, nil
}

func parseAPIFieldLine(line string) (apiField, error) {
	label, path, labeled := splitLabel(line)
	if !labeled {
		path = strings.TrimSpace(line)
	}
	if labeled && label == "" {
		return apiField{}, errors.New("请填写显示名")
	}
	if path == "" {
		return apiField{}, errors.New("请填写字段路径")
	}
	if err := checkAPIField(path); err != nil {
		return apiField{}, err
	}
	if utf8.RuneCountInString(label) > 40 {
		return apiField{}, errors.New("显示名太长")
	}
	return apiField{Label: label, Path: path}, nil
}

func splitLabel(line string) (label, path string, labeled bool) {
	for _, sep := range []string{"=", "＝"} {
		if i := strings.Index(line, sep); i >= 0 {
			return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+len(sep):]), true
		}
	}
	return "", "", false
}

func checkAPIField(path string) error {
	if strings.ContainsAny(path, " \t") {
		return fmt.Errorf("字段 %s 不能有空格", path)
	}
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			return fmt.Errorf("字段 %s 的写法不对", path)
		}
	}
	if utf8.RuneCountInString(path) > 200 {
		return fmt.Errorf("字段名称太长")
	}
	return nil
}

func trimPrefixFold(line, prefix string) (string, bool) {
	if len(line) < len(prefix) || !strings.EqualFold(line[:len(prefix)], prefix) {
		return "", false
	}
	return line[len(prefix):], true
}

// EncodeAPI 把窗口里分开填的内容写成任务正文。
func EncodeAPI(form APIForm, requireFields bool) (string, error) {
	method := strings.ToUpper(strings.TrimSpace(form.Method))
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodPost {
		return "", errors.New("请求方法只能是 GET 或 POST")
	}
	parsed, err := parsePageURL(form.URL)
	if err != nil {
		return "", errors.New("接口地址要写成 http 或 https 开头的网址")
	}
	headers, err := ParseHeaderRows(form.Headers)
	if err != nil {
		return "", err
	}
	fields, err := ParseFieldRows(form.Fields)
	if err != nil {
		return "", err
	}
	enabledFields := 0
	for _, field := range fields {
		if field.Enabled && strings.TrimSpace(field.Value) != "" {
			enabledFields++
		}
	}
	if requireFields && enabledFields == 0 {
		return "", errors.New("请填写要发送的字段，例如 data.total")
	}
	var b strings.Builder
	if method == http.MethodPost {
		b.WriteString("POST ")
	}
	b.WriteString(parsed.String())
	b.WriteString("\n")
	for _, line := range splitPlainLines(form.Params) {
		key, val, _ := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		fmt.Fprintf(&b, "// Param: %s=%s\n", key, strings.TrimSpace(val))
	}
	for _, header := range headers {
		if !header.Enabled || strings.TrimSpace(header.Key) == "" {
			if strings.TrimSpace(header.Key) != "" {
				fmt.Fprintf(&b, "// Header: %s: %s\n", header.Key, header.Value)
			}
			continue
		}
		fmt.Fprintf(&b, "Header: %s: %s\n", header.Key, header.Value)
	}
	body := strings.TrimSpace(form.Body)
	if method == http.MethodPost && body != "" {
		if strings.Contains(body, "\n") {
			b.WriteString("Body<<\n")
			b.WriteString(body)
			if !strings.HasSuffix(body, "\n") {
				b.WriteString("\n")
			}
			b.WriteString("Body>>\n")
		} else {
			b.WriteString("Body: ")
			b.WriteString(body)
			b.WriteString("\n")
		}
	}
	for _, field := range fields {
		path := strings.TrimSpace(field.Value)
		if path == "" {
			continue
		}
		line := path
		if label := strings.TrimSpace(field.Key); label != "" && label != path {
			line = label + " = " + path
		}
		if !field.Enabled {
			line = "// Field: " + line
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()), nil
}

func splitPlainLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// DecodeAPI 把已经保存的任务正文拆回窗口里的各项。解析不了时保留原文，避免内容丢掉。
func DecodeAPI(body string) APIForm {
	body = strings.TrimSpace(body)
	if body == "" {
		return APIForm{Method: http.MethodGet}
	}
	target, err := parseAPITarget(body, false)
	if err != nil {
		return APIForm{Method: http.MethodGet, Fields: body}
	}
	form := APIForm{Method: target.Method, URL: target.URL, Body: target.Body}
	if form.Method == "" {
		form.Method = http.MethodGet
	}
	var params []string
	for _, row := range target.OffParams {
		params = append(params, row.Key+"="+row.Value)
	}
	form.Params = strings.Join(params, "\n")
	var headers []string
	for _, header := range target.Headers {
		headers = append(headers, header.Name+": "+header.Value)
	}
	for _, header := range target.OffHeaders {
		headers = append(headers, "// "+header.Name+": "+header.Value)
	}
	form.Headers = strings.Join(headers, "\n")
	var fields []string
	for _, field := range target.Fields {
		if field.Label != "" && field.Label != field.Path {
			fields = append(fields, field.Label+" = "+field.Path)
			continue
		}
		fields = append(fields, field.Path)
	}
	for _, field := range target.OffFields {
		line := field.Path
		if field.Label != "" && field.Label != field.Path {
			line = field.Label + " = " + field.Path
		}
		fields = append(fields, "// "+line)
	}
	form.Fields = strings.Join(fields, "\n")
	return form
}

// MergeAPIFields 把试取列出的字段补进已有字段，已经写过的路径不会再加一遍。
func MergeAPIFields(current, suggested string) string {
	var lines []string
	seen := map[string]bool{}
	for _, line := range splitPlainLines(current) {
		lines = append(lines, line)
		seen[fieldPath(line)] = true
	}
	for _, line := range splitPlainLines(suggested) {
		path := fieldPath(line)
		if path == "" || seen[path] {
			continue
		}
		lines = append(lines, line)
		seen[path] = true
	}
	return strings.Join(lines, "\n")
}

func fieldPath(line string) string {
	_, path, labeled := splitLabel(line)
	if !labeled {
		path = strings.TrimSpace(line)
	}
	return path
}

// FetchAPI 请求接口。勾选了字段时只发送这些字段；一个都没勾时，发送格式化后的整段 JSON。
func FetchAPI(ctx context.Context, body string) (string, error) {
	target, err := parseAPITarget(body, false)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	target = applyAPIVars(target)
	status, raw, err := doAPI(ctx, target)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("接口返回 %d", status)
	}
	payload, err := decodeJSON(raw)
	if err != nil {
		return "", err
	}
	if len(target.Fields) == 0 {
		return strings.TrimSpace(prettyJSON(raw)), nil
	}
	return renderFields(target.Fields, payload), nil
}

// PreviewAPI 请求接口。还没填字段时也会返回可以选用的路径。
func PreviewAPI(ctx context.Context, body string) (APIPreview, error) {
	target, err := parseAPITarget(body, false)
	if err != nil {
		return APIPreview{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	vars := apiVarNote(target)
	target = applyAPIVars(target)
	started := time.Now()
	status, raw, err := doAPI(ctx, target)
	if err != nil {
		return APIPreview{}, err
	}
	var preview APIPreview
	preview.Status = status
	preview.Millis = time.Since(started).Milliseconds()
	preview.Raw = prettyJSON(raw)
	preview.Vars = vars
	payload, jerr := decodeJSON(raw)
	if jerr == nil {
		if status >= 200 && status < 300 {
			if len(target.Fields) > 0 {
				preview.Message = renderFields(target.Fields, payload)
			} else {
				preview.Message = strings.TrimSpace(preview.Raw)
			}
		}
		suggestions := suggestFields(payload)
		var paths []string
		var detail []string
		for _, item := range suggestions {
			paths = append(paths, item.path)
			detail = append(detail, item.path+" = "+item.sample)
		}
		preview.Fields = strings.Join(paths, "\n")
		preview.Choices = detail
	}
	var text strings.Builder
	if preview.Message != "" {
		if len(target.Fields) == 0 {
			text.WriteString("没有勾选字段，会把整段 JSON 发给微信。\n\n")
		}
		text.WriteString("会发给微信：\n\n")
		text.WriteString(preview.Message)
		text.WriteString("\n\n")
	} else {
		text.WriteString("还没有选择字段。\n\n")
	}
	if len(preview.Choices) == 0 {
		text.WriteString("接口里没有能选用的字段。")
	} else {
		text.WriteString("可以选用的字段：\n")
		text.WriteString(strings.Join(preview.Choices, "\n"))
	}
	preview.Detail = text.String()
	return preview, nil
}

var apiClock = time.Now

func applyAPIVars(target apiTarget) apiTarget {
	now := apiClock()
	target.URL = expandAPIText(target.URL, now)
	target.Body = expandAPIText(target.Body, now)
	for i := range target.Headers {
		target.Headers[i].Name = expandAPIText(target.Headers[i].Name, now)
		target.Headers[i].Value = expandAPIText(target.Headers[i].Value, now)
	}
	return target
}

func apiDateVars(now time.Time) [][2]string {
	yesterday := now.AddDate(0, 0, -1)
	return [][2]string{
		{"yesterday-yyyy-mm-dd", yesterday.Format("2006-01-02")},
		{"today-yyyy-mm-dd", now.Format("2006-01-02")},
		{"yesterday", yesterday.Format("20060102")},
		{"today", now.Format("20060102")},
	}
}

func apiVarNote(target apiTarget) string {
	var blob strings.Builder
	blob.WriteString(target.URL)
	blob.WriteString("\n")
	blob.WriteString(target.Body)
	for _, header := range target.Headers {
		blob.WriteString("\n")
		blob.WriteString(header.Name)
		blob.WriteString(header.Value)
	}
	text := blob.String()
	var parts []string
	for _, item := range apiDateVars(apiClock()) {
		if apiTokenUsed(text, item[0]) {
			parts = append(parts, "{"+item[0]+"}="+item[1])
		}
	}
	return strings.Join(parts, "，")
}

func apiTokenUsed(text, name string) bool {
	if strings.Contains(text, "{"+name+"}") {
		return true
	}
	encoded := "%7B" + name + "%7D"
	return strings.Contains(text, encoded) || strings.Contains(text, strings.ToLower(encoded))
}

func expandAPIText(text string, now time.Time) string {
	if text == "" {
		return ""
	}
	pairs := make([]string, 0, 16)
	for _, item := range apiDateVars(now) {
		token := "{" + item[0] + "}"
		encoded := "%7B" + item[0] + "%7D"
		pairs = append(pairs, token, item[1], encoded, item[1], strings.ToLower(encoded), item[1])
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

func doAPI(ctx context.Context, target apiTarget) (int, []byte, error) {
	method := target.Method
	if method == "" {
		method = http.MethodGet
	}
	var reader io.Reader
	if method == http.MethodPost {
		reader = strings.NewReader(target.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.URL, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json, text/json;q=0.9, */*;q=0.1")
	for _, header := range target.Headers {
		req.Header.Set(header.Name, header.Value)
	}
	if method == http.MethodPost && strings.TrimSpace(target.Body) != "" && req.Header.Get("Content-Type") == "" {
		trimmed := strings.TrimSpace(target.Body)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			req.Header.Set("Content-Type", "application/json; charset=utf-8")
		}
	}
	resp, err := pageClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, errPageTimeout
		}
		return 0, nil, errors.New("接口请求失败")
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, maxPageBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return resp.StatusCode, nil, errors.New("接口没有返回完整内容")
	}
	if len(raw) > maxPageBytes {
		return resp.StatusCode, nil, errors.New("接口返回过长")
	}
	return resp.StatusCode, raw, nil
}

func decodeJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var payload any
	if err := dec.Decode(&payload); err != nil {
		return nil, errors.New("接口没有返回 JSON")
	}
	return payload, nil
}

func prettyJSON(raw []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimSpace(raw), "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

func renderFields(fields []apiField, payload any) string {
	var lines []string
	for _, field := range fields {
		values, total, found := collectJSON(payload, strings.Split(field.Path, "."))
		lines = append(lines, renderField(fieldLabel(field), values, total, found))
	}
	return strings.Join(lines, "\n")
}

func fieldLabel(field apiField) string {
	if strings.TrimSpace(field.Label) != "" {
		return field.Label
	}
	return field.Path
}

func renderField(label string, values []any, total int, found bool) string {
	if !found {
		return label + ": （没有这个字段）"
	}
	if total == 0 || len(values) == 0 {
		return label + ": （没有内容）"
	}
	if len(values) == 1 && isScalar(values[0]) {
		return label + ": " + formatScalar(values[0])
	}
	var b strings.Builder
	b.WriteString(label)
	b.WriteString(":\n")
	if len(values) == 1 {
		writeValue(&b, values[0], "  ", 0)
	} else {
		writeList(&b, values, total, "  ", 0)
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeList(b *strings.Builder, values []any, total int, indent string, depth int) {
	for _, value := range values {
		if isScalar(value) {
			fmt.Fprintf(b, "%s- %s\n", indent, formatScalar(value))
			continue
		}
		if obj, ok := value.(map[string]any); ok {
			writeObjectItem(b, obj, indent, depth)
			continue
		}
		b.WriteString(indent)
		b.WriteString("- \n")
		writeValue(b, value, indent+"  ", depth+1)
	}
	if total > len(values) {
		fmt.Fprintf(b, "%s…还有 %d 条\n", indent, total-len(values))
	}
}

func writeObjectItem(b *strings.Builder, obj map[string]any, indent string, depth int) {
	keys := sortedKeys(obj)
	b.WriteString(indent)
	b.WriteString("- ")
	if len(keys) == 0 {
		b.WriteString("（空）\n")
		return
	}
	writeKeyed(b, keys[0], obj[keys[0]], "", indent+"  ", depth)
	for _, key := range keys[1:] {
		writeKeyed(b, key, obj[key], indent+"  ", indent+"    ", depth)
	}
}

func writeKeyed(b *strings.Builder, key string, value any, prefix, childIndent string, depth int) {
	if isScalar(value) {
		fmt.Fprintf(b, "%s%s: %s\n", prefix, key, formatScalar(value))
		return
	}
	fmt.Fprintf(b, "%s%s:\n", prefix, key)
	writeValue(b, value, childIndent, depth+1)
}

func writeValue(b *strings.Builder, value any, indent string, depth int) {
	if depth > 4 {
		fmt.Fprintf(b, "%s%s\n", indent, compactJSON(value))
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := sortedKeys(typed)
		if len(keys) == 0 {
			fmt.Fprintf(b, "%s（空）\n", indent)
			return
		}
		for _, key := range keys {
			writeKeyed(b, key, typed[key], indent, indent+"  ", depth)
		}
	case []any:
		if len(typed) == 0 {
			fmt.Fprintf(b, "%s（没有内容）\n", indent)
			return
		}
		limit := len(typed)
		if limit > maxAPIItems {
			limit = maxAPIItems
		}
		writeList(b, typed[:limit], len(typed), indent, depth)
	default:
		fmt.Fprintf(b, "%s%s\n", indent, formatScalar(value))
	}
}

func collectJSON(node any, parts []string) (values []any, total int, found bool) {
	if len(parts) == 0 {
		return []any{node}, 1, true
	}
	part := parts[0]
	rest := parts[1:]
	switch typed := node.(type) {
	case map[string]any:
		if part == "*" {
			return nil, 0, false
		}
		next, ok := typed[part]
		if !ok {
			return nil, 0, false
		}
		return collectJSON(next, rest)
	case []any:
		if part != "*" {
			index, err := parseIndex(part, len(typed))
			if err != nil {
				return nil, 0, false
			}
			return collectJSON(typed[index], rest)
		}
		if len(rest) == 0 {
			limit := len(typed)
			if limit > maxAPIItems {
				limit = maxAPIItems
			}
			return append([]any(nil), typed[:limit]...), len(typed), true
		}
		var all []any
		count := 0
		anyFound := false
		for _, item := range typed {
			got, n, ok := collectJSON(item, rest)
			if !ok {
				continue
			}
			anyFound = true
			count += n
			if len(all) < maxAPIItems {
				room := maxAPIItems - len(all)
				if len(got) > room {
					got = got[:room]
				}
				all = append(all, got...)
			}
		}
		if !anyFound {
			if len(typed) == 0 {
				return []any{}, 0, true
			}
			return nil, 0, false
		}
		return all, count, true
	default:
		return nil, 0, false
	}
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

func isScalar(value any) bool {
	switch value.(type) {
	case nil, string, bool, json.Number:
		return true
	default:
		return false
	}
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

func formatScalar(value any) string {
	text := formatJSONValue(value)
	text = strings.ReplaceAll(text, "\r\n", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text, _ = truncateRunes(text, 1000)
	return text
}

func compactJSON(value any) string {
	text, _ := truncateRunes(formatJSONValue(value), 120)
	return text
}

func sortedKeys(obj map[string]any) []string {
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type suggestion struct {
	path   string
	sample string
}

func suggestFields(node any) []suggestion {
	var out []suggestion
	walkSuggest(node, "", &out, 0)
	return out
}

func walkSuggest(node any, prefix string, out *[]suggestion, depth int) {
	if len(*out) >= maxAPIFields || depth > 6 {
		return
	}
	switch typed := node.(type) {
	case map[string]any:
		keys := sortedKeys(typed)
		if len(keys) == 0 && prefix != "" {
			*out = append(*out, suggestion{path: prefix, sample: "（空）"})
			return
		}
		for _, key := range keys {
			if len(*out) >= maxAPIFields {
				return
			}
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			walkSuggest(typed[key], path, out, depth+1)
		}
	case []any:
		path := "*"
		if prefix != "" {
			path = prefix + ".*"
		}
		if len(typed) == 0 {
			*out = append(*out, suggestion{path: path, sample: "（没有内容）"})
			return
		}
		if allScalar(typed) {
			*out = append(*out, suggestion{path: path, sample: joinSamples(typed)})
			return
		}
		merged := map[string]any{}
		for i, item := range typed {
			if i >= 5 {
				break
			}
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			for key, value := range obj {
				if _, exists := merged[key]; !exists {
					merged[key] = value
				}
			}
		}
		if len(merged) == 0 {
			*out = append(*out, suggestion{path: path, sample: joinSamples(typed)})
			return
		}
		walkSuggest(merged, path, out, depth+1)
	default:
		if prefix == "" {
			return
		}
		*out = append(*out, suggestion{path: prefix, sample: oneLine(formatScalar(node), 60)})
	}
}

func allScalar(values []any) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if !isScalar(value) {
			return false
		}
	}
	return true
}

func joinSamples(values []any) string {
	limit := len(values)
	if limit > 3 {
		limit = 3
	}
	parts := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		parts = append(parts, oneLine(formatScalar(values[i]), 20))
	}
	text := strings.Join(parts, "、")
	if len(values) > limit {
		text += "、…"
	}
	return text
}

func oneLine(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	text, _ = truncateRunes(text, limit)
	if text == "" {
		return "（空）"
	}
	return text
}
