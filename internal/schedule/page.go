package schedule

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const maxPageBytes = 2 << 20

var errPageTimeout = errors.New("超时")

var pageClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("跳转次数过多")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return errors.New("只打开 http 或 https 页面")
		}
		return nil
	},
}

func parsePageURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("页面地址要写成 http 或 https 开头的网址")
	}
	if parsed.User != nil {
		return nil, errors.New("页面地址不要带账号密码")
	}
	return parsed, nil
}

type pageTarget struct {
	URL    string
	Cookie string
}

func parsePageBody(body string) (pageTarget, error) {
	var target pageTarget
	var cookies []string
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) >= len("cookie:") && strings.EqualFold(line[:len("cookie:")], "cookie:") {
			cookies = append(cookies, strings.TrimSpace(line[len("cookie:"):]))
			continue
		}
		if target.URL != "" {
			return pageTarget{}, errors.New("网址下面只能再写 Cookie: 开头的一行")
		}
		target.URL = line
	}
	parsed, err := parsePageURL(target.URL)
	if err != nil {
		return pageTarget{}, err
	}
	target.URL = parsed.String()
	target.Cookie = strings.Join(cookies, "; ")
	return target, nil
}

// FetchPage 打开网址，并读出页面正文里的文字。
// 源码里几乎没有文字、要靠脚本画出来的页面，会用本机 Edge 或 Chrome 跑完再读。
func FetchPage(ctx context.Context, body string) (string, error) {
	target, err := parsePageBody(body)
	if err != nil {
		return "", err
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(parent, 75*time.Second)
	defer cancel()

	text, htmlSrc, kind, err := downloadPage(ctx, target)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded && parent.Err() == nil {
			return "", errPageTimeout
		}
		return "", err
	}
	if kind == "html" && shouldRender(htmlSrc, text) {
		rendered, rerr := renderWithBrowser(ctx, target)
		if rerr != nil {
			if utf8.RuneCountInString(text) >= 80 {
				return text, nil
			}
			if parent.Err() != nil {
				return "", parent.Err()
			}
			return "", rerr
		}
		rendered = plainText(rendered)
		if utf8.RuneCountInString(rendered) > utf8.RuneCountInString(text) {
			return rendered, nil
		}
	}
	return strings.TrimSpace(text), nil
}

func downloadPage(ctx context.Context, target pageTarget) (text, htmlSrc, kind string, err error) {
	httpCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(httpCtx, http.MethodGet, target.URL, nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	if target.Cookie != "" {
		req.Header.Set("Cookie", target.Cookie)
	}
	resp, err := pageClient.Do(req)
	if err != nil {
		if httpCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			return "", "", "", errPageTimeout
		}
		return "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", "", fmt.Errorf("页面返回 %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return "", "", "", err
	}
	if len(raw) > maxPageBytes {
		return "", "", "", errors.New("页面太大")
	}
	contentType := resp.Header.Get("Content-Type")
	kind = pageKind(contentType, raw)
	switch kind {
	case "image":
		return "", "", kind, errors.New("这个地址是图片，没有读出文字")
	case "binary":
		return "", "", kind, errors.New("这个页面不是文字页面")
	case "text":
		decoded, err := decodePage(raw, contentType)
		if err != nil {
			return "", "", kind, err
		}
		return plainText(decoded), "", kind, nil
	default:
		decoded, err := decodePage(raw, contentType)
		if err != nil {
			return "", "", kind, err
		}
		return extractHTMLText(decoded), decoded, "html", nil
	}
}

func shouldRender(htmlSrc, text string) bool {
	if !looksLikeScriptApp(htmlSrc) {
		return false
	}
	return utf8.RuneCountInString(strings.TrimSpace(text)) < 80
}

func looksLikeScriptApp(src string) bool {
	s := strings.ToLower(src)
	if !strings.Contains(s, "<script") {
		return false
	}
	return strings.Contains(s, "id=\"app\"") || strings.Contains(s, "id='app'") ||
		strings.Contains(s, "id=\"root\"") || strings.Contains(s, "type=\"module\"")
}

func pageKind(contentType string, body []byte) string {
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		media = ""
	}
	media = strings.ToLower(strings.TrimSpace(media))
	switch {
	case strings.HasPrefix(media, "image/"):
		return "image"
	case media == "text/html" || media == "application/xhtml+xml":
		return "html"
	case strings.HasPrefix(media, "text/") || media == "application/json" || media == "application/xml" || media == "application/xhtml+xml":
		return "text"
	case media == "":
		if looksLikeHTML(body) {
			return "html"
		}
		if looksLikeText(body) {
			return "text"
		}
		return "binary"
	default:
		if looksLikeHTML(body) {
			return "html"
		}
		return "binary"
	}
}

func looksLikeHTML(body []byte) bool {
	sample := body
	if len(sample) > 2048 {
		sample = sample[:2048]
	}
	s := strings.ToLower(string(sample))
	return strings.Contains(s, "<html") || strings.Contains(s, "<body") || strings.Contains(s, "<p") || strings.Contains(s, "<div") || strings.Contains(s, "<article")
}

func looksLikeText(body []byte) bool {
	if len(body) == 0 {
		return true
	}
	sample := body
	if len(sample) > 2048 {
		sample = sample[:2048]
	}
	if utf8.Valid(sample) {
		return true
	}
	controls := 0
	for _, b := range sample {
		if b < 0x09 || (b > 0x0d && b < 0x20) {
			controls++
		}
	}
	return controls*20 < len(sample)
}

func decodePage(body []byte, contentType string) (string, error) {
	preview := body
	if len(preview) > 1024 {
		preview = preview[:1024]
	}
	_, name, certain := charset.DetermineEncoding(preview, contentType)
	if !certain && !utf8.Valid(body) && (name == "windows-1252" || name == "utf-8" || name == "") {
		contentType = "text/html; charset=gb18030"
	}
	reader, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return "", err
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func extractHTMLText(src string) string {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return plainText(src)
	}
	title := cleanText(nodeText(findTag(doc, "title")))
	root := findTag(doc, "body")
	if root == nil {
		root = doc
	}
	if chosen := preferContent(root); chosen != nil {
		root = chosen
	}
	var buf textBuf
	walkText(root, &buf)
	text := strings.TrimSpace(buf.String())
	if text == "" {
		text = metaDescription(doc)
	}
	if title != "" && text != "" && !strings.Contains(text, title) {
		return title + "\n" + text
	}
	if text == "" {
		return title
	}
	return text
}

func preferContent(body *html.Node) *html.Node {
	for _, tag := range []string{"article", "main"} {
		node := findTag(body, tag)
		if node == nil {
			continue
		}
		var buf textBuf
		walkText(node, &buf)
		if utf8.RuneCountInString(strings.TrimSpace(buf.String())) >= 8 {
			return node
		}
	}
	return body
}

func walkText(n *html.Node, buf *textBuf) {
	if n == nil {
		return
	}
	if n.Type == html.ElementNode {
		if skipTag(n) {
			return
		}
		if n.Data == "img" {
			buf.breakLine()
			buf.text(attr(n, "alt"))
			buf.breakLine()
			return
		}
		block := blockTag(n.Data)
		if block {
			buf.breakLine()
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walkText(child, buf)
		}
		if block {
			buf.breakLine()
		}
		return
	}
	if n.Type == html.TextNode {
		buf.text(n.Data)
		return
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		walkText(child, buf)
	}
}

func skipTag(n *html.Node) bool {
	switch n.Data {
	case "script", "style", "noscript", "svg", "template", "nav", "footer", "iframe", "canvas":
		return true
	}
	return strings.EqualFold(attr(n, "role"), "navigation")
}

func blockTag(tag string) bool {
	switch tag {
	case "address", "article", "aside", "blockquote", "br", "dd", "div", "dl", "dt",
		"figcaption", "figure", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hr",
		"li", "main", "ol", "p", "pre", "section", "table", "tr", "ul":
		return true
	default:
		return false
	}
}

func metaDescription(doc *html.Node) string {
	var found string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != "" || n == nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "meta" {
			name := strings.ToLower(attr(n, "name"))
			property := strings.ToLower(attr(n, "property"))
			if name == "description" || property == "og:description" {
				found = cleanText(attr(n, "content"))
				return
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return found
}

func findTag(n *html.Node, tag string) *html.Node {
	if n == nil {
		return nil
	}
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if found := findTag(child, tag); found != nil {
			return found
		}
	}
	return nil
}

func nodeText(n *html.Node) string {
	if n == nil {
		return ""
	}
	var buf textBuf
	walkText(n, &buf)
	return buf.String()
}

func attr(n *html.Node, key string) string {
	for _, item := range n.Attr {
		if strings.EqualFold(item.Key, key) {
			return item.Val
		}
	}
	return ""
}

func cleanText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func plainText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			blank++
			if blank > 1 || len(out) == 0 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

type textBuf struct {
	b      strings.Builder
	atLine bool
}

func (t *textBuf) breakLine() {
	if t.b.Len() == 0 || t.atLine {
		return
	}
	t.b.WriteByte('\n')
	t.atLine = true
}

func (t *textBuf) text(s string) {
	s = cleanText(s)
	if s == "" {
		return
	}
	if t.b.Len() > 0 && !t.atLine {
		t.b.WriteByte(' ')
	}
	t.b.WriteString(s)
	t.atLine = false
}

func (t *textBuf) String() string { return t.b.String() }
