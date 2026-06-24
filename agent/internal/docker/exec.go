// Package docker -- ExecRun runs a one-shot command inside a running
// container via the Docker exec API, capturing stdout/stderr (each
// truncated at a fixed byte budget) and the exit code.
package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	dockerclient "github.com/moby/moby/client"
)

// Per-stream byte budgets for captured exec output. Larger streams are
// truncated and the corresponding *Truncated flag on ExecResult is set.
const (
	execStdoutBudgetBytes = 30 * 1024 // 30 KB
	execStderrBudgetBytes = 10 * 1024 // 10 KB
)

// Bounds for the per-exec timeout. The agent clamps caller-provided
// values into this range to keep a single exec from monopolising the
// command runner.
const (
	execTimeoutMinSeconds = 1
	execTimeoutMaxSeconds = 300
)

// ExecSpec describes a single command to run inside a container.
type ExecSpec struct {
	// Cmd is the argv vector passed to the container's exec endpoint.
	// Typical use: []string{"sh", "-c", "<shell command>"}.
	Cmd []string

	// Env is a list of KEY=VALUE strings injected into the exec process.
	Env []string

	// WorkingDir is the absolute path inside the container to chdir to
	// before running Cmd. Empty means the image default.
	WorkingDir string

	// TimeoutSeconds caps the exec wall-clock duration. Values outside
	// [execTimeoutMinSeconds, execTimeoutMaxSeconds] are clamped.
	TimeoutSeconds int

	// CPUBurst is an OPTIONAL flag (omitted = false = no burst). When true,
	// the container's CPU cap is raised to a burst value for the duration of
	// this exec, then restored to the original cap via a defer. Used by the
	// web export to speed cold Metro transforms (~2200 framework modules at
	// the 0.5-CPU baseline take minutes). Honoured only by the concrete
	// dockerClient.ExecRun (which holds the moby SDK handle needed for
	// ContainerUpdate); fail-safe — any burst/restore error is logged and the
	// exec proceeds at the current cap, never aborting.
	CPUBurst bool
}

// ExecResult captures the outcome of an exec invocation.
type ExecResult struct {
	// ExitCode mirrors the container exec exit code. -1 indicates the
	// exec was aborted by the agent's timeout watchdog.
	ExitCode int

	// Stdout is the captured stdout bytes, truncated at the per-stream
	// budget. StdoutTruncated is true when data was dropped.
	Stdout          []byte
	StdoutTruncated bool

	// Stderr is the captured stderr bytes (also truncated). When the
	// exec times out, a trailer line is appended documenting the cause.
	Stderr          []byte
	StderrTruncated bool

	// DurationMs is the wall-clock execution time observed by the agent.
	DurationMs int64
}

// execClient is the subset of the moby SDK Client used by ExecRun.
// It is satisfied by *dockerclient.Client and a fake in unit tests.
type execClient interface {
	ExecCreate(ctx context.Context, containerID string, options dockerclient.ExecCreateOptions) (dockerclient.ExecCreateResult, error)
	ExecAttach(ctx context.Context, execID string, options dockerclient.ExecAttachOptions) (dockerclient.ExecAttachResult, error)
	ExecInspect(ctx context.Context, execID string, options dockerclient.ExecInspectOptions) (dockerclient.ExecInspectResult, error)
}

// ExecRun runs spec.Cmd inside containerID and returns the captured
// streams + exit code. It enforces a per-exec timeout, demultiplexes
// stdout/stderr, and truncates each stream at a fixed budget so a
// runaway command can't blow up the ack payload.
//
// On timeout the function still returns a non-nil ExecResult: ExitCode
// is set to -1 and a trailer is appended to Stderr. The error return is
// reserved for transport / API failures (ExecCreate/ExecAttach errors).
func ExecRun(ctx context.Context, client execClient, containerID string, spec ExecSpec) (*ExecResult, error) {
	if len(spec.Cmd) == 0 {
		return nil, errors.New("docker: ExecRun: empty Cmd")
	}

	timeoutSec := spec.TimeoutSeconds
	if timeoutSec < execTimeoutMinSeconds {
		timeoutSec = execTimeoutMinSeconds
	}
	if timeoutSec > execTimeoutMaxSeconds {
		timeoutSec = execTimeoutMaxSeconds
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	createResp, err := client.ExecCreate(execCtx, containerID, dockerclient.ExecCreateOptions{
		Cmd:          spec.Cmd,
		Env:          spec.Env,
		WorkingDir:   spec.WorkingDir,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, fmt.Errorf("docker: exec create on %s: %w", containerID, err)
	}

	start := time.Now()

	attachResp, err := client.ExecAttach(execCtx, createResp.ID, dockerclient.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("docker: exec attach on %s: %w", createResp.ID, err)
	}
	defer attachResp.Close()

	stdoutBuf := newLimitedBuffer(execStdoutBudgetBytes)
	stderrBuf := newLimitedBuffer(execStderrBudgetBytes)

	copyDone := make(chan error, 1)
	go func() {
		_, copyErr := stdcopy.StdCopy(stdoutBuf, stderrBuf, attachResp.Reader)
		copyDone <- copyErr
	}()

	timedOut := false
	select {
	case <-copyDone:
		// stream drained naturally
	case <-execCtx.Done():
		// Timeout or upstream cancellation. Close the hijacked
		// connection so StdCopy returns; ignore the error since we
		// already know the cause.
		attachResp.Close()
		<-copyDone
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			timedOut = true
		}
	}

	durationMs := time.Since(start).Milliseconds()

	result := &ExecResult{
		Stdout:          stdoutBuf.Bytes(),
		StdoutTruncated: stdoutBuf.Truncated(),
		Stderr:          stderrBuf.Bytes(),
		StderrTruncated: stderrBuf.Truncated(),
		DurationMs:      durationMs,
	}

	if timedOut {
		result.ExitCode = -1
		trailer := []byte(fmt.Sprintf("\n[execution timed out after %ds]\n", timeoutSec))
		result.Stderr = append(result.Stderr, trailer...)
		return result, nil
	}

	// Inspect with the parent ctx so we still get an exit code even if
	// execCtx has been cancelled by the timeout path above.
	inspectResp, err := client.ExecInspect(ctx, createResp.ID, dockerclient.ExecInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("docker: exec inspect on %s: %w", createResp.ID, err)
	}
	result.ExitCode = inspectResp.ExitCode
	return result, nil
}

// ── limitedBuffer ────────────────────────────────────────────────────────

// limitedBuffer is an io.Writer that caps total bytes written. Writes
// past the budget are silently dropped and Truncated() returns true.
// It exists so a misbehaving exec can't pin unbounded memory in the
// agent.
type limitedBuffer struct {
	buf       bytes.Buffer
	maxBytes  int
	truncated bool
}

func newLimitedBuffer(maxBytes int) *limitedBuffer {
	return &limitedBuffer{maxBytes: maxBytes}
}

// Write implements io.Writer. It writes as much of p as fits into the
// remaining byte budget. Once the budget is reached, subsequent calls
// are no-ops aside from setting truncated=true. Write never returns an
// error so stdcopy.StdCopy keeps draining the multiplexed stream until
// EOF instead of bailing on a short write.
func (l *limitedBuffer) Write(p []byte) (int, error) {
	remaining := l.maxBytes - l.buf.Len()
	if remaining <= 0 {
		l.truncated = true
		return len(p), nil
	}
	if len(p) <= remaining {
		return l.buf.Write(p)
	}
	if _, err := l.buf.Write(p[:remaining]); err != nil {
		return 0, err
	}
	l.truncated = true
	return len(p), nil
}

// Bytes returns the captured bytes (up to maxBytes).
func (l *limitedBuffer) Bytes() []byte { return l.buf.Bytes() }

// Truncated reports whether any bytes were dropped by Write.
func (l *limitedBuffer) Truncated() bool { return l.truncated }

// Compile-time check that limitedBuffer satisfies io.Writer.
var _ io.Writer = (*limitedBuffer)(nil)
