//go:build !windows

package services

import (
	"os/exec"
	"syscall"
	"time"
)

// prepareBackgroundCmd puts the child in its own process group so cancel can
// reach the ffmpeg process yt-dlp starts during post-processing.
func prepareBackgroundCmd(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

const (
	processTreeGrace = 2 * time.Second
	processTreePoll  = 50 * time.Millisecond
)

// terminateProcessTree asks the whole process group to stop, waits briefly for
// yt-dlp and its ffmpeg child to exit, then forces the group down. SIGTERM
// gives yt-dlp the best chance to leave .part files intact for resume; SIGKILL
// is only the fallback for a child that ignores it.
func terminateProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return
	}
	pgid := cmd.Process.Pid
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
		return
	}
	deadline := time.Now().Add(processTreeGrace)
	for {
		if err := syscall.Kill(-pgid, 0); err == syscall.ESRCH {
			return
		}
		if !time.Now().Before(deadline) {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			return
		}
		time.Sleep(processTreePoll)
	}
}
