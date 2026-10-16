package schedule

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const macroFileName = "macros.json"

// Macro 是几个定时任务共用的参数。任务正文里写 {名称}，运行时再换成这里的值。
type Macro struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// LoadMacros 读取数据目录里的 macros.json。文件不存在时返回空列表。
func LoadMacros(dir string) ([]Macro, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, macroFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	var stored struct {
		Items []Macro `json:"items"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, errors.New("宏文件损坏")
	}
	return stored.Items, nil
}

// SaveMacros 把宏写到数据目录的 macros.json。任务里仍然保存 {名称}，不写成具体的值。
func SaveMacros(dir string, items []Macro) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("没有数据目录")
	}
	cleaned, err := normalizeMacros(items)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(struct {
		Items []Macro `json:"items"`
	}{Items: cleaned}, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writePrivate(filepath.Join(dir, macroFileName), raw)
}

// ParseMacroText 读取编辑框里的宏。一行一个，写成 token=密钥。
func ParseMacroText(text string) ([]Macro, error) {
	var items []Macro
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, errors.New("每行写成 名称=值，例如 token=密钥")
		}
		items = append(items, Macro{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value)})
	}
	return normalizeMacros(items)
}

// FormatMacroText 把宏排成编辑框里的一行一个。
func FormatMacroText(items []Macro) string {
	var b strings.Builder
	for _, item := range items {
		if strings.TrimSpace(item.Name) == "" {
			continue
		}
		b.WriteString(item.Name)
		b.WriteString("=")
		b.WriteString(item.Value)
		b.WriteString("\n")
	}
	return b.String()
}

// ExpandMacros 把 {名称} 换成宏的值。没有定义的名称保持原样。日期变量不在这里处理。
func ExpandMacros(text string, items []Macro) string {
	if text == "" || len(items) == 0 {
		return text
	}
	pairs := make([]string, 0, len(items)*6)
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		token := "{" + name + "}"
		encoded := "%7B" + name + "%7D"
		pairs = append(pairs, token, item.Value, encoded, item.Value, strings.ToLower(encoded), item.Value)
	}
	if len(pairs) == 0 {
		return text
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

func (b *Book) applyMacros(task Task) (Task, error) {
	if b == nil {
		return task, nil
	}
	items, err := LoadMacros(b.Dir())
	if err != nil {
		return task, err
	}
	task.Body = ExpandMacros(task.Body, items)
	task.Prefix = ExpandMacros(task.Prefix, items)
	return task, nil
}

func normalizeMacros(items []Macro) ([]Macro, error) {
	if len(items) > 50 {
		return nil, errors.New("宏最多 50 个")
	}
	seen := map[string]bool{}
	cleaned := make([]Macro, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		value := strings.TrimSpace(item.Value)
		if name == "" && value == "" {
			continue
		}
		if err := validateMacroName(name); err != nil {
			return nil, err
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, errors.New("宏的值不能换行")
		}
		if utf8.RuneCountInString(value) > 4000 {
			return nil, errors.New("宏的值太长")
		}
		if seen[name] {
			return nil, errors.New("宏「" + name + "」重复了")
		}
		seen[name] = true
		cleaned = append(cleaned, Macro{Name: name, Value: value})
	}
	return cleaned, nil
}

func validateMacroName(name string) error {
	if name == "" {
		return errors.New("请填写宏的名称")
	}
	if utf8.RuneCountInString(name) > 32 {
		return errors.New("宏的名称最多 32 个字")
	}
	switch name {
	case "today", "yesterday", "today-yyyy-mm-dd", "yesterday-yyyy-mm-dd":
		return errors.New("「" + name + "」是日期变量，不能当作宏")
	}
	for i, r := range name {
		if r == '_' || (r == '-' && i > 0) {
			continue
		}
		if unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r)) {
			continue
		}
		return errors.New("宏的名称用字母、数字、下划线或短横线，例如 token")
	}
	return nil
}
