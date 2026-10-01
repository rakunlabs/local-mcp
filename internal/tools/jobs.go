package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rakunlabs/local-mcp/internal/output"
)

// Jobs owns background shell commands. They live as long as the server, not
// the request that started them, and are stopped when the server shuts down.
type Jobs struct {
	ctx    context.Context
	output *output.Store
	seq    atomic.Uint64

	mu   sync.Mutex
	jobs map[string]*Job
}

type Job struct {
	ID       string
	Command  string
	Dir      string
	PID      int
	Output   string
	Started  time.Time
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	done     chan struct{}
	finished time.Time
	exitCode int
	state    string
}

func NewJobs(ctx context.Context, store *output.Store) *Jobs {
	return &Jobs{ctx: ctx, output: store, jobs: map[string]*Job{}}
}

func (j *Jobs) Start(command, dir, shell string, timeout time.Duration) (*Job, error) {
	out, err := j.output.Create()
	if err != nil {
		return nil, err
	}

	var (
		ctx    context.Context
		cancel context.CancelFunc
	)

	if timeout > 0 {
		ctx, cancel = context.WithTimeout(j.ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(j.ctx)
	}

	cmd := newCommand(ctx, shell, command, dir)
	cmd.Stdout = out
	cmd.Stderr = out

	if err := cmd.Start(); err != nil {
		cancel()
		_ = out.Close()
		_ = os.Remove(out.Name())

		return nil, fmt.Errorf("unable to start command; %w", err)
	}

	job := &Job{
		ID:      fmt.Sprintf("job_%d", j.seq.Add(1)),
		Command: command,
		Dir:     dir,
		PID:     cmd.Process.Pid,
		Output:  out.Name(),
		Started: time.Now(),
		cmd:     cmd,
		cancel:  cancel,
		done:    make(chan struct{}),
		state:   "running",
	}

	j.mu.Lock()
	j.jobs[job.ID] = job
	j.mu.Unlock()

	go func() {
		err := cmd.Wait()
		_ = out.Close()

		j.mu.Lock()
		job.finished = time.Now()
		job.exitCode = -1

		var exitErr *exec.ExitError

		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			job.state = "timed out"
		case job.state == "killing" || ctx.Err() != nil:
			job.state = "killed"
		case err == nil:
			job.state, job.exitCode = "exited", 0
		case errors.As(err, &exitErr):
			job.state, job.exitCode = "exited", exitErr.ExitCode()
		default:
			job.state = "failed: " + err.Error()
		}
		j.mu.Unlock()

		cancel()
		close(job.done)
	}()

	return job, nil
}

func (j *Jobs) get(id string) (*Job, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	job, ok := j.jobs[id]
	if !ok {
		return nil, fmt.Errorf("unknown job %q", id)
	}

	return job, nil
}

func (j *Jobs) describe(job *Job) string {
	j.mu.Lock()
	defer j.mu.Unlock()

	elapsed := time.Since(job.Started)
	if !job.finished.IsZero() {
		elapsed = job.finished.Sub(job.Started)
	}

	s := fmt.Sprintf("%s [%s] pid=%d elapsed=%s", job.ID, job.state, job.PID, elapsed.Round(time.Millisecond))
	if job.state == "exited" {
		s += fmt.Sprintf(" exit=%d", job.exitCode)
	}

	return s + "\n  command: " + job.Command + "\n  output: " + job.Output
}

// Close stops every running job.
func (j *Jobs) Close() {
	j.mu.Lock()
	jobs := make([]*Job, 0, len(j.jobs))
	for _, job := range j.jobs {
		jobs = append(jobs, job)
	}
	j.mu.Unlock()

	for _, job := range jobs {
		job.cancel()
	}

	for _, job := range jobs {
		select {
		case <-job.done:
		case <-time.After(5 * time.Second):
		}
	}
}

type ShellJobInput struct {
	Action  string `json:"action" jsonschema:"One of: list, status, wait, kill"`
	ID      string `json:"id,omitempty" jsonschema:"Job id returned by shell with background: true. Required except for list."`
	Timeout int    `json:"timeout,omitempty" jsonschema:"For wait: how long to wait in milliseconds before returning (default 30000, at most 600000)."`
}

const shellJobDescription = `Manage background jobs started by shell with background: true.
- list: every job with its state.
- status: state plus the latest output of one job.
- wait: block until the job finishes or the timeout passes, then report status and output.
- kill: stop the job and its whole process group.
Full output stays in the job's output file, which read and grep can open.`

func registerShellJob(server *mcp.Server, d *Deps) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "shell_job",
		Description: shellJobDescription,
		Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ShellJobInput) (*mcp.CallToolResult, any, error) {
		res, err := d.shellJob(ctx, in)

		return res, nil, err
	})
}

func (d *Deps) shellJob(ctx context.Context, in ShellJobInput) (*mcp.CallToolResult, error) {
	j := d.Jobs

	if in.Action == "list" {
		j.mu.Lock()
		jobs := make([]*Job, 0, len(j.jobs))
		for _, job := range j.jobs {
			jobs = append(jobs, job)
		}
		j.mu.Unlock()

		if len(jobs) == 0 {
			return text("No background jobs"), nil
		}

		sort.Slice(jobs, func(a, b int) bool { return jobs[a].Started.Before(jobs[b].Started) })

		lines := make([]string, len(jobs))
		for i, job := range jobs {
			lines[i] = j.describe(job)
		}

		return text(strings.Join(lines, "\n")), nil
	}

	if in.ID == "" {
		return nil, errors.New("id is required")
	}

	job, err := j.get(in.ID)
	if err != nil {
		return nil, err
	}

	switch in.Action {
	case "status":
	case "wait":
		timeout := time.Duration(in.Timeout) * time.Millisecond
		if timeout <= 0 {
			timeout = 30 * time.Second
		}

		timeout = min(timeout, 10*time.Minute)

		select {
		case <-job.done:
		case <-time.After(timeout):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case "kill":
		j.mu.Lock()
		if job.state == "running" {
			job.state = "killing"
		}
		j.mu.Unlock()

		job.cancel()

		select {
		case <-job.done:
		case <-time.After(5 * time.Second):
		}
	default:
		return nil, fmt.Errorf("unknown action %q; use list, status, wait or kill", in.Action)
	}

	res, err := d.Output.TailFile(job.Output, true)
	if err != nil {
		return nil, fmt.Errorf("read job output; %w", err)
	}

	body := res.Content
	if strings.TrimSpace(body) == "" {
		body = "(no output yet)"
	}

	return text(j.describe(job) + "\n\n" + body), nil
}
