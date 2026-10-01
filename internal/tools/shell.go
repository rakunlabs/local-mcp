package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ShellInput struct {
	Command    string `json:"command" jsonschema:"Shell command string to execute"`
	Workdir    string `json:"workdir,omitempty" jsonschema:"Working directory. Defaults to the workspace root; relative paths resolve from it. Use this instead of cd in the command."`
	Timeout    int    `json:"timeout,omitempty" jsonschema:"Timeout in milliseconds. Foreground commands default to 120000. Background commands have no timeout unless one is given."`
	Background bool   `json:"background,omitempty" jsonschema:"Run in the background and return a job id immediately. Use for dev servers and long-running builds; inspect it with shell_job."`
}

func shellDescription(d *Deps) string {
	shell := d.shellPath()

	return fmt.Sprintf(`Execute a shell command and return its combined stdout and stderr.
Runs with the host user's filesystem, process and network authority. OS: %s, Shell: %s.
The workspace root is the default working directory; set workdir instead of using cd. Stdin is closed, so interactive commands fail.
Foreground commands time out after %d ms unless timeout is set. When the timeout passes, the whole process group is stopped.
Set background to true for dev servers and other long-running processes; it returns a job id, and shell_job lists, inspects, waits for and stops jobs.
Output over %d lines or %d bytes is cut to its last part, and the full output is saved to a file you can grep or read.
Prefer the dedicated read, glob, grep, edit, write and patch tools over shell for file operations.
Do not chain shell commands with separators like echo "===="; it only adds noise.`,
		runtime.GOOS, shell, d.Config.Shell.DefaultTimeoutMS, d.Config.Output.MaxLines, d.Config.Output.MaxBytes)
}

func registerShell(server *mcp.Server, d *Deps) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "shell",
		Description: shellDescription(d),
		Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(true)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ShellInput) (*mcp.CallToolResult, any, error) {
		res, err := d.shell(ctx, in)

		return res, nil, err
	})
}

func (d *Deps) shellPath() string {
	if d.Config.Shell.Path != "" {
		return d.Config.Shell.Path
	}

	return defaultShell()
}

func (d *Deps) shell(ctx context.Context, in ShellInput) (*mcp.CallToolResult, error) {
	if strings.TrimSpace(in.Command) == "" {
		return nil, errors.New("command is required")
	}

	if in.Timeout < 0 {
		return nil, fmt.Errorf("invalid timeout %d; must be positive milliseconds", in.Timeout)
	}

	if max := d.Config.Shell.MaxTimeoutMS; max > 0 && in.Timeout > max {
		return nil, fmt.Errorf("timeout %d ms exceeds the maximum of %d ms", in.Timeout, max)
	}

	dir, err := d.Files.Resolve(in.Workdir)
	if err != nil {
		return nil, err
	}

	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("working directory is not a directory: %s", in.Workdir)
	}

	if in.Background {
		job, err := d.Jobs.Start(in.Command, dir, d.shellPath(), time.Duration(in.Timeout)*time.Millisecond)
		if err != nil {
			return nil, err
		}

		return text(fmt.Sprintf("Started background job %s (pid %d).\nOutput file: %s\n"+
			"Use shell_job with action status, wait or kill and id %q.", job.ID, job.PID, job.Output, job.ID)), nil
	}

	timeout := in.Timeout
	if timeout == 0 {
		timeout = d.Config.Shell.DefaultTimeoutMS
	}

	return d.runForeground(ctx, in.Command, dir, time.Duration(timeout)*time.Millisecond)
}

func (d *Deps) runForeground(ctx context.Context, command, dir string, timeout time.Duration) (*mcp.CallToolResult, error) {
	out, err := d.Output.Create()
	if err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := newCommand(runCtx, d.shellPath(), command, dir)
	cmd.Stdout = out
	cmd.Stderr = out

	runErr := cmd.Run()
	_ = out.Close()

	var (
		exitCode = -1
		meta     []string
	)

	var exitErr *exec.ExitError

	switch {
	case runErr == nil:
		exitCode = 0
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		meta = append(meta, fmt.Sprintf("Command was terminated after exceeding the timeout of %d ms. "+
			"If it is expected to take longer and is not waiting for input, retry with a larger timeout or background: true.",
			timeout.Milliseconds()))
	case ctx.Err() != nil:
		meta = append(meta, "Command was cancelled by the client.")
	case errors.As(runErr, &exitErr):
		exitCode = exitErr.ExitCode()
	default:
		_ = os.Remove(out.Name())

		return nil, fmt.Errorf("unable to execute command; %w", runErr)
	}

	res, err := d.Output.TailFile(out.Name(), false)
	if err != nil {
		return nil, fmt.Errorf("read command output; %w", err)
	}

	body := strings.TrimRight(res.Content, "\n")
	if strings.TrimSpace(body) == "" {
		body = "(no output)"
	}

	if exitCode >= 0 {
		meta = append(meta, fmt.Sprintf("Exit code: %d", exitCode))
	}

	body += "\n\n<shell_metadata>\n" + strings.Join(meta, "\n") + "\n</shell_metadata>"

	result := text(body)
	result.IsError = exitCode != 0

	return result, nil
}

func newCommand(ctx context.Context, shell, command, dir string) *exec.Cmd {
	args := shellArgs(shell, command)

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	prepareProcess(cmd)

	return cmd
}
