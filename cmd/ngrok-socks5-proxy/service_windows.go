//go:build windows

package main

import "golang.org/x/sys/windows"

// isElevated reports whether the current process is running with
// Administrator privileges, required for registering/controlling a
// Windows Service.
func isElevated() bool {
	token := windows.GetCurrentProcessToken()
	return token.IsElevated()
}
