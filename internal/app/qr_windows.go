//go:build windows

package app

import (
	"os/exec"
	"syscall"
	"unsafe"
)

var (
	kernel32                   = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode         = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode         = kernel32.NewProc("SetConsoleMode")
	procGetStdHandle           = kernel32.NewProc("GetStdHandle")
	procGetConsoleScreenBuffer = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

func init() {
	consoleColumns = windowsConsoleWidth
}

func enableTerminalQR() bool {
	handle := stdoutHandle()
	var mode uint32
	r, _, _ := procGetConsoleMode.Call(handle, uintptr(unsafe.Pointer(&mode)))
	if r == 0 {
		return false
	}
	const enableVirtualTerminalProcessing = 0x0004
	r, _, _ = procSetConsoleMode.Call(handle, uintptr(mode|enableVirtualTerminalProcessing))
	return r != 0
}

func windowsConsoleWidth() int {
	handle := stdoutHandle()
	var info consoleScreenBufferInfo
	r, _, _ := procGetConsoleScreenBuffer.Call(handle, uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 80
	}
	width := int(info.Window.Right - info.Window.Left + 1)
	if width <= 0 {
		return 80
	}
	return width
}

func stdoutHandle() uintptr {
	const stdOutputHandle = ^uintptr(10) // -11
	handle, _, _ := procGetStdHandle.Call(stdOutputHandle)
	return handle
}

type coord struct {
	X int16
	Y int16
}

type smallRect struct {
	Left   int16
	Top    int16
	Right  int16
	Bottom int16
}

type consoleScreenBufferInfo struct {
	Size              coord
	CursorPosition    coord
	Attributes        uint16
	Window            smallRect
	MaximumWindowSize coord
}

func openImage(path string) error {
	paint := exec.Command("mspaint", path)
	if err := paint.Start(); err == nil {
		return nil
	}
	return exec.Command("cmd", "/c", "start", "", path).Start()
}
