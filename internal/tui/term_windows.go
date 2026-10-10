//go:build windows

package tui

import (
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode    = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode    = kernel32.NewProc("SetConsoleMode")
	procGetStdHandle      = kernel32.NewProc("GetStdHandle")
	procGetConsoleScreen  = kernel32.NewProc("GetConsoleScreenBufferInfo")
	procGetNumberOfInput  = kernel32.NewProc("GetNumberOfConsoleInputEvents")
	procReadConsoleInputW = kernel32.NewProc("ReadConsoleInputW")
)

func interactive() bool {
	handle := stdHandle(uintptr(0xfffffff5)) // STD_OUTPUT_HANDLE = -11
	var mode uint32
	r, _, _ := procGetConsoleMode.Call(handle, uintptr(unsafe.Pointer(&mode)))
	return r != 0
}

func enableVT() {
	handle := stdHandle(uintptr(0xfffffff5))
	var mode uint32
	r, _, _ := procGetConsoleMode.Call(handle, uintptr(unsafe.Pointer(&mode)))
	if r == 0 {
		return
	}
	const enableVirtualTerminalProcessing = 0x0004
	_, _, _ = procSetConsoleMode.Call(handle, uintptr(mode|enableVirtualTerminalProcessing))
}

func windowSize() (int, int) {
	handle := stdHandle(uintptr(0xfffffff5))
	var info consoleScreenBufferInfo
	r, _, _ := procGetConsoleScreen.Call(handle, uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 80, 24
	}
	width := int(info.Window.Right - info.Window.Left + 1)
	height := int(info.Window.Bottom - info.Window.Top + 1)
	if width < 40 {
		width = 40
	}
	if height < 8 {
		height = 8
	}
	return width, height
}

func readKey(timeout time.Duration) (press, error) {
	handle := stdHandle(uintptr(0xfffffff6)) // STD_INPUT_HANDLE = -10
	deadline := time.Now().Add(timeout)
	for {
		pressed, ok, err := pollKey(handle)
		if err != nil {
			return press{}, err
		}
		if ok {
			return pressed, nil
		}
		if time.Now().After(deadline) {
			return press{kind: keyNone}, nil
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func pollKey(handle uintptr) (press, bool, error) {
	var count uint32
	r, _, err := procGetNumberOfInput.Call(handle, uintptr(unsafe.Pointer(&count)))
	if r == 0 {
		return press{}, false, err
	}
	if count == 0 {
		return press{}, false, nil
	}
	var rec inputRecord
	var read uint32
	r, _, err = procReadConsoleInputW.Call(handle, uintptr(unsafe.Pointer(&rec)), 1, uintptr(unsafe.Pointer(&read)))
	if r == 0 {
		return press{}, false, err
	}
	if rec.EventType != 0x0001 || read == 0 {
		return press{}, false, nil
	}
	down := *(*int32)(unsafe.Pointer(&rec.Info[0])) != 0
	if !down {
		return press{}, false, nil
	}
	vk := *(*uint16)(unsafe.Pointer(&rec.Info[6]))
	char := rune(*(*uint16)(unsafe.Pointer(&rec.Info[10])))
	switch vk {
	case 0x26:
		return press{kind: keyUp}, true, nil
	case 0x28:
		return press{kind: keyDown}, true, nil
	case 0x25:
		return press{kind: keyLeft}, true, nil
	case 0x27:
		return press{kind: keyRight}, true, nil
	case 0x1B:
		return press{kind: keyQuit}, true, nil
	case 0x0D:
		return press{kind: keyEnter}, true, nil
	case 0x08:
		return press{kind: keyBackspace}, true, nil
	}
	if char >= 32 {
		return press{kind: keyRune, char: char}, true, nil
	}
	return press{}, false, nil
}

func stdHandle(kind uintptr) uintptr {
	handle, _, _ := procGetStdHandle.Call(kind)
	return handle
}

type inputRecord struct {
	EventType uint16
	_         [2]byte
	Info      [16]byte
}

type coord struct {
	X int16
	Y int16
}

type smallRect struct {
	Left, Top, Right, Bottom int16
}

type consoleScreenBufferInfo struct {
	Size              coord
	CursorPosition    coord
	Attributes        uint16
	Window            smallRect
	MaximumWindowSize coord
}
