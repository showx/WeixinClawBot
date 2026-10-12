//go:build !windows

package schedule

import (
	"context"
	"errors"
)

func readOpenPage(context.Context, string, string) (string, error) {
	return "", errors.New("当前系统不能读取已打开的浏览器")
}
