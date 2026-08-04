//go:build !windows

package main

import "os"

// isElevated reports whether the current process has root privileges,
// required for installing/controlling a systemd or launchd service.
func isElevated() bool {
	return os.Geteuid() == 0
}
