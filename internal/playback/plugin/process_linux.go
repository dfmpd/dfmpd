//go:build linux

package plugin

import (
	"errors"
	"os/exec"
	"syscall"
)

func isolateProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

// Observe exit without reaping the group leader. Keeping it waitable reserves
// its PID/PGID until the last group signal, avoiding signals to a reused group.
// Linux permits a nil siginfo pointer; only completion is needed here.
func observeExit(cmd *exec.Cmd) error {
	for {
		_, _, err := syscall.Syscall6(syscall.SYS_WAITID, 1 /* P_PID */, uintptr(cmd.Process.Pid), 0, syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		if err == syscall.EINTR {
			continue
		}
		if err != 0 {
			return err
		}
		return nil
	}
}

func signalGroup(cmd *exec.Cmd, force bool) error {
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	err := syscall.Kill(-cmd.Process.Pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
