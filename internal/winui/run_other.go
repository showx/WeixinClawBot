//go:build !windows

package winui

import (
	"errors"

	"weixinclawbot/internal/app"
)

// Run 在非 Windows 上没有桌面窗口。
func Run(*app.App, string, string) error {
	return errors.New("桌面程序目前只提供 Windows 版本")
}
