//go:build !windows

package tools

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

func defaultShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}

	return "/bin/sh"
}

func shellArgs(shell, command string) []string {
	return []string{shell, "-c", command}
}

// prepareProcess puts the command in its own process group so a timeout or
// cancellation can stop everything it started.
func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killTree(cmd) }
	cmd.WaitDelay = 3 * time.Second
}

func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}

	pgid := -cmd.Process.Pid

	_ = syscall.Kill(pgid, syscall.SIGTERM)

	go func() {
		time.Sleep(3 * time.Second)

		_ = syscall.Kill(pgid, syscall.SIGKILL)
	}()

	return nil
}
