//go:build !windows

package services

import "os/exec"

// prepareBackgroundCmd is a no-op on non-Windows operating systems where
// console windows are not automatically spawned by GUI apps.
func prepareBackgroundCmd(cmd *exec.Cmd) {
}
