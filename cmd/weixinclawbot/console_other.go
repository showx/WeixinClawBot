//go:build !windows

package main

func noConsole() bool { return false }
