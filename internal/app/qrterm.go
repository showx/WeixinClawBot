package app

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/skip2/go-qrcode"
)

var consoleColumns = func() int { return 120 }

func writeQRFile(dir, contentURL string) (string, error) {
	path, err := filepath.Abs(filepath.Join(dir, "login-qr.png"))
	if err != nil {
		return "", err
	}
	if err := qrcode.WriteFile(contentURL, qrcode.Medium, 512, path); err != nil {
		return "", err
	}
	return path, nil
}

func writeTerminalQR(out io.Writer, content string) error {
	code, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return err
	}
	bitmap := code.Bitmap()
	if len(bitmap) == 0 || len(bitmap[0]) == 0 {
		return fmt.Errorf("二维码内容为空")
	}
	need := len(bitmap[0]) * 2
	if width := consoleColumns(); width < need {
		return fmt.Errorf("命令行窗口只有 %d 列，画不下二维码。请扫描弹出的图片；也可以把窗口拉宽到 %d 列后重新 login", width, need)
	}
	const (
		dark  = "\x1b[40m  "
		light = "\x1b[107m  "
		reset = "\x1b[0m\n"
	)
	for _, row := range bitmap {
		for _, module := range row {
			if module {
				_, _ = io.WriteString(out, dark)
			} else {
				_, _ = io.WriteString(out, light)
			}
		}
		_, _ = io.WriteString(out, reset)
	}
	return nil
}
