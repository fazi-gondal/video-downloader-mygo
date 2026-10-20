//go:build windows

package services

import (
	"os/exec"
	"testing"
)

func TestPrepareBackgroundCmdWindows(t *testing.T) {
	cmd := exec.Command("dummy")
	prepareBackgroundCmd(cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("expected non-nil SysProcAttr on Windows")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Error("expected HideWindow to be true on Windows")
	}
	if cmd.SysProcAttr.CreationFlags&0x08000000 == 0 {
		t.Errorf("expected CreationFlags to have CREATE_NO_WINDOW (0x08000000), got %x", cmd.SysProcAttr.CreationFlags)
	}
}
