//go:build windows

package services

import (
	"os/exec"
	"strconv"
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

// terminateProcessTree uses taskkill /T to include yt-dlp's ffmpeg child. If
// taskkill is unavailable or the process already exited, fall back to the
// direct process handle.
func terminateProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return
	}
	killer := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	prepareBackgroundCmd(killer)
	if err := killer.Run(); err != nil {
		_ = cmd.Process.Kill()
	}
}
