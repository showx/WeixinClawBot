//go:build !windows

package tui

import "time"

func interactive() bool { return false }

func enableVT() {}

func windowSize() (int, int) { return 80, 24 }

func readKey(timeout time.Duration) (press, error) {
	time.Sleep(timeout)
	return press{kind: keyNone}, nil
}
