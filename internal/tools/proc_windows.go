//go:build windows

package tools

import (
	"os"
	"os/exec"
	"strconv"
	"time"
)

func defaultShell() string {
	if s := os.Getenv("COMSPEC"); s != "" {
		return s
	}

	return "cmd.exe"
}

func shellArgs(shell, command string) []string {
	return []string{shell, "/C", command}
}

func prepareProcess(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return killTree(cmd) }
	cmd.WaitDelay = 3 * time.Second
}

func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}

	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}
