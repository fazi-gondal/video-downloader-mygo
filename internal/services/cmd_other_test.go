//go:build !windows

package services

import (
	"os/exec"
	"testing"
)

func TestPrepareBackgroundCmdOther(t *testing.T) {
	cmd := exec.Command("dummy")
	prepareBackgroundCmd(cmd)
	// prepareBackgroundCmd is safe to call and does not panic on non-Windows.
}
