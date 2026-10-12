//go:build windows

package main

import "syscall"

func noConsole() bool {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow")
	hwnd, _, _ := proc.Call()
	return hwnd == 0
}
