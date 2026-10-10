//go:build !windows

package app

import "os/exec"

func enableTerminalQR() bool { return true }

func openImage(path string) error {
	return exec.Command("xdg-open", path).Start()
}
