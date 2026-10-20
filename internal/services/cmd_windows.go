//go:build windows

package services

import (
	"os/exec"
	"syscall"
)

// prepareBackgroundCmd configures Windows-specific process attributes so child
// console binaries (yt-dlp, ffmpeg, ffprobe) run silently without allocating
// or showing a console window.
func prepareBackgroundCmd(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= 0x08000000 // CREATE_NO_WINDOW
}
