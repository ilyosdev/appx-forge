package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/appx/forge/agent/internal/controlclient"
	"github.com/appx/forge/agent/internal/docker"
	"github.com/appx/forge/agent/internal/ports"
)

// ── Mock Docker Client ─────────────────────────────────────────────────

type mockDockerClient struct {
	mu sync.Mutex

	createContainerFn   func(ctx context.Context, spec *docker.SandboxSpec) (string, error)
	stopContainerFn     func(ctx context.Context, containerID string, timeout time.Duration) error
	removeContainerFn   func(ctx context.Context, containerID string) error
	restartContainerFn  func(ctx context.Context, containerID string, timeout time.Duration) error
	getLogsFn           func(ctx context.Context, containerID string, tail int, follow bool) (io.ReadCloser, error)
	startContainerFn    func(ctx context.Context, containerID string) error
	listContainersFn    func(ctx context.Context) ([]docker.ContainerSnapshot, error)
	execRunFn           func(ctx context.Context, containerID string, spec docker.ExecSpec) (*docker.ExecResult, error)
	createBuildWorkerFn func(ctx context.Context, spec *docker.BuildWorkerSpec) (string, error)
	listBuildWorkersFn  func(ctx context.Context) ([]docker.BuildWorkerInfo, error)

	// Track calls for assertions
	createCalls  []docker.SandboxSpec
	stopCalls    []string
	removeCalls  []string
	restartCalls []string
	logCalls     []string
	execCalls    []execCall
	startCalls   []string
}

// execCall captures one invocation of mockDockerClient.ExecRun so tests
// can assert against the resolved container ID and ExecSpec.
type execCall struct {
	ContainerID string
	Spec        docker.ExecSpec
}

func newMockDockerClient() *mockDockerClient {
	return &mockDockerClient{}
}

func (m *mockDockerClient) CreateContainer(ctx context.Context, spec *docker.SandboxSpec) (string, error) {
	m.mu.Lock()
	if spec != nil {
		m.createCalls = append(m.createCalls, *spec)
	}
	m.mu.Unlock()
	if m.createContainerFn != nil {
		return m.createContainerFn(ctx, spec)
	}
	return "container-abc123", nil
}

func (m *mockDockerClient) StartContainer(ctx context.Context, containerID string) error {
	m.mu.Lock()
	m.startCalls = append(m.startCalls, containerID)
	m.mu.Unlock()
	if m.startContainerFn != nil {
		return m.startContainerFn(ctx, containerID)
	}
	return nil
}

func (m *mockDockerClient) StopContainer(ctx context.Context, containerID string, timeout time.Duration) error {
	m.mu.Lock()
	m.stopCalls = append(m.stopCalls, containerID)
	m.mu.Unlock()
	if m.stopContainerFn != nil {
		return m.stopContainerFn(ctx, containerID, timeout)
	}
	return nil
}

func (m *mockDockerClient) RemoveContainer(ctx context.Context, containerID string) error {
	m.mu.Lock()
	m.removeCalls = append(m.removeCalls, containerID)
	m.mu.Unlock()
	if m.removeContainerFn != nil {
		return m.removeContainerFn(ctx, containerID)
	}
	return nil
}

func (m *mockDockerClient) RestartContainer(ctx context.Context, containerID string, timeout time.Duration) error {
	m.mu.Lock()
	m.restartCalls = append(m.restartCalls, containerID)
	m.mu.Unlock()
	if m.restartContainerFn != nil {
		return m.restartContainerFn(ctx, containerID, timeout)
	}
	return nil
}

func (m *mockDockerClient) InspectContainer(ctx context.Context, containerID string) (*docker.ContainerInfo, error) {
	return &docker.ContainerInfo{ID: containerID, State: "running", Running: true}, nil
}

func (m *mockDockerClient) GetLogs(ctx context.Context, containerID string, tail int, follow bool) (io.ReadCloser, error) {
	m.mu.Lock()
	m.logCalls = append(m.logCalls, containerID)
	m.mu.Unlock()
	if m.getLogsFn != nil {
		return m.getLogsFn(ctx, containerID, tail, follow)
	}
	return io.NopCloser(strings.NewReader("line 1\nline 2\nline 3\n")), nil
}

func (m *mockDockerClient) PullImage(ctx context.Context, imageRef string) error {
	return nil
}

func (m *mockDockerClient) EventsStream(ctx context.Context, since time.Time) (<-chan docker.ContainerEvent, <-chan error) {
	ch := make(chan docker.ContainerEvent)
	errCh := make(chan error)
	close(ch)
	close(errCh)
	return ch, errCh
}

func (m *mockDockerClient) ListContainers(ctx context.Context) ([]docker.ContainerSnapshot, error) {
	if m.listContainersFn != nil {
		return m.listContainersFn(ctx)
	}
	return []docker.ContainerSnapshot{}, nil
}

func (m *mockDockerClient) ExecRun(ctx context.Context, containerID string, spec docker.ExecSpec) (*docker.ExecResult, error) {
	m.mu.Lock()
	m.execCalls = append(m.execCalls, execCall{ContainerID: containerID, Spec: spec})
	fn := m.execRunFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, containerID, spec)
	}
	return &docker.ExecResult{ExitCode: 0}, nil
}

func (m *mockDockerClient) CreateBuildWorker(ctx context.Context, spec *docker.BuildWorkerSpec) (string, error) {
	if m.createBuildWorkerFn != nil {
		return m.createBuildWorkerFn(ctx, spec)
	}
	return "build-worker-abc123", nil
}

func (m *mockDockerClient) ListBuildWorkers(ctx context.Context) ([]docker.BuildWorkerInfo, error) {
	if m.listBuildWorkersFn != nil {
		return m.listBuildWorkersFn(ctx)
	}
	return []docker.BuildWorkerInfo{}, nil
}

func (m *mockDockerClient) Close() error {
	return nil
}

// ── Mock Ack Reporter ──────────────────────────────────────────────────

type mockAckReporter struct {
	mu   sync.Mutex
	acks []ackCall
}

type ackCall struct {
	cmdID  string
	status string
	errMsg string
	result interface{}
}

func (m *mockAckReporter) AckCommand(ctx context.Context, cmdID string, ack controlclient.AckRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acks = append(m.acks, ackCall{
		cmdID:  cmdID,
		status: ack.Status,
		errMsg: ack.Error,
		result: ack.Result,
	})
	return nil
}

func (m *mockAckReporter) lastAck() ackCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.acks) == 0 {
		return ackCall{}
	}
	return m.acks[len(m.acks)-1]
}

// ── Helpers ────────────────────────────────────────────────────────────

func makeCommand(cmdType, sandboxID string, payload interface{}) controlclient.Command {
	p, _ := json.Marshal(payload)
	return controlclient.Command{
		ID:             "cmd-" + cmdType,
		Type:           cmdType,
		SandboxID:      sandboxID,
		Payload:        p,
		IssuedAt:       time.Now(),
		TimeoutSeconds: 60,
	}
}

func newTestExecutor(dc docker.Client, ack *mockAckReporter) *CommandExecutor {
	portAlloc := ports.NewAllocator(40000, 40010)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewCommandExecutor(dc, portAlloc, ack, "/tmp/forge-test", logger)
}

// ── Tests ──────────────────────────────────────────────────────────────

func TestExecuteStartSandbox(t *testing.T) {
	dc := newMockDockerClient()
	ack := &mockAckReporter{}
	exec := newTestExecutor(dc, ack)

	payload := map[string]interface{}{
		"app_name": "my-app",
		"image":    "appx/sandbox:v1",
		"resources": map[string]interface{}{
			"cpu_cores": 0.5,
			"memory_mb": 512,
		},
		"env": map[string]string{
			"APP_NAME": "my-app",
		},
	}

	cmd := makeCommand("start_sandbox", "sandbox-123", payload)
	err := exec.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	// Verify Docker was called
	dc.mu.Lock()
	if len(dc.createCalls) != 1 {
		t.Fatalf("expected 1 CreateContainer call, got %d", len(dc.createCalls))
	}
	spec := dc.createCalls[0]
	dc.mu.Unlock()

	if spec.AppName != "my-app" {
		t.Errorf("expected AppName=my-app, got %s", spec.AppName)
	}
	if spec.Image != "appx/sandbox:v1" {
		t.Errorf("expected Image=appx/sandbox:v1, got %s", spec.Image)
	}
	if spec.HostPort < 40000 || spec.HostPort > 40010 {
		t.Errorf("expected HostPort in [40000,40010], got %d", spec.HostPort)
	}

	// Verify ack
	a := ack.lastAck()
	if a.status != "success" {
		t.Errorf("expected ack status=success, got %s", a.status)
	}

	// Verify sandbox tracked
	exec.mu.RLock()
	info, ok := exec.sandboxes["sandbox-123"]
	exec.mu.RUnlock()
	if !ok {
		t.Fatal("sandbox-123 not tracked")
	}
	if info.ContainerID != "container-abc123" {
		t.Errorf("expected ContainerID=container-abc123, got %s", info.ContainerID)
	}
}

func TestExecuteStartSandboxFailure(t *testing.T) {
	dc := newMockDockerClient()
	dc.createContainerFn = func(ctx context.Context, spec *docker.SandboxSpec) (string, error) {
		return "", errors.New("image not found")
	}
	ack := &mockAckReporter{}
	exec := newTestExecutor(dc, ack)

	payload := map[string]interface{}{
		"app_name": "fail-app",
		"image":    "appx/sandbox:v99",
		"resources": map[string]interface{}{
			"cpu_cores": 0.5,
			"memory_mb": 512,
		},
	}

	cmd := makeCommand("start_sandbox", "sandbox-fail", payload)
	err := exec.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Execute should not return error (it acks failure): %v", err)
	}

	a := ack.lastAck()
	if a.status != "failure" {
		t.Errorf("expected ack status=failure, got %s", a.status)
	}
	if a.errMsg == "" {
		t.Error("expected non-empty error message in ack")
	}
}

func TestExecuteStopSandbox(t *testing.T) {
	dc := newMockDockerClient()
	ack := &mockAckReporter{}
	exec := newTestExecutor(dc, ack)

	// Pre-populate sandbox map
	exec.mu.Lock()
	exec.sandboxes["sandbox-stop"] = &sandboxInfo{
		ContainerID: "container-stop",
		HostPort:    40001,
		AppName:     "stop-app",
	}
	exec.mu.Unlock()

	// Need to allocate port 40001 first so Release succeeds
	exec.ports.AllocateSpecific(40001)

	payload := map[string]interface{}{
		"container_id": "container-stop",
	}

	cmd := makeCommand("stop_sandbox", "sandbox-stop", payload)
	err := exec.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	dc.mu.Lock()
	if len(dc.stopCalls) != 1 || dc.stopCalls[0] != "container-stop" {
		t.Errorf("expected StopContainer(container-stop), got %v", dc.stopCalls)
	}
	if len(dc.removeCalls) != 1 || dc.removeCalls[0] != "container-stop" {
		t.Errorf("expected RemoveContainer(container-stop), got %v", dc.removeCalls)
	}
	dc.mu.Unlock()

	a := ack.lastAck()
	if a.status != "success" {
		t.Errorf("expected ack status=success, got %s", a.status)
	}

	// Verify sandbox removed from map
	exec.mu.RLock()
	_, ok := exec.sandboxes["sandbox-stop"]
	exec.mu.RUnlock()
	if ok {
		t.Error("expected sandbox-stop to be removed from map")
	}
}

func TestExecuteRestartSandbox(t *testing.T) {
	dc := newMockDockerClient()
	ack := &mockAckReporter{}
	exec := newTestExecutor(dc, ack)

	// Pre-populate sandbox map
	exec.mu.Lock()
	exec.sandboxes["sandbox-restart"] = &sandboxInfo{
		ContainerID: "container-restart",
		HostPort:    40002,
		AppName:     "restart-app",
	}
	exec.mu.Unlock()

	payload := map[string]interface{}{
		"container_id": "container-restart",
	}

	cmd := makeCommand("restart_sandbox", "sandbox-restart", payload)
	err := exec.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	dc.mu.Lock()
	if len(dc.restartCalls) != 1 || dc.restartCalls[0] != "container-restart" {
		t.Errorf("expected RestartContainer(container-restart), got %v", dc.restartCalls)
	}
	dc.mu.Unlock()

	a := ack.lastAck()
	if a.status != "success" {
		t.Errorf("expected ack status=success, got %s", a.status)
	}
}

// TestRestartSandbox_DebouncesBurst proves the structural-push restart is
// coalesced: a burst of RestartSandbox calls (a gen's clustered syncs) results in
// exactly ONE docker restart, fired after the debounce window — not one per call.
func TestRestartSandbox_DebouncesBurst(t *testing.T) {
	old := restartDebounce
	restartDebounce = 40 * time.Millisecond
	defer func() { restartDebounce = old }()

	dc := newMockDockerClient()
	exec := newTestExecutor(dc, &mockAckReporter{})
	exec.mu.Lock()
	exec.sandboxes["sbx"] = &sandboxInfo{ContainerID: "ctr", HostPort: 40003, AppName: "app"}
	exec.mu.Unlock()

	// Burst of 5 structural pushes within the window.
	for i := 0; i < 5; i++ {
		if err := exec.RestartSandbox("sbx"); err != nil {
			t.Fatalf("RestartSandbox returned error: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Nothing should have restarted yet (still inside the debounce window).
	dc.mu.Lock()
	n := len(dc.restartCalls)
	dc.mu.Unlock()
	if n != 0 {
		t.Errorf("expected 0 restarts during the burst, got %d", n)
	}

	// After the window settles, exactly one coalesced restart.
	time.Sleep(120 * time.Millisecond)
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if len(dc.restartCalls) != 1 || dc.restartCalls[0] != "ctr" {
		t.Errorf("expected exactly one coalesced RestartContainer(ctr), got %v", dc.restartCalls)
	}
}

// TestRestartSandbox_SeparateBurstsEachRestart proves bursts separated by more
// than the window each get their own restart (one per settle, no permanent
// suppression).
func TestRestartSandbox_SeparateBurstsEachRestart(t *testing.T) {
	old := restartDebounce
	restartDebounce = 30 * time.Millisecond
	defer func() { restartDebounce = old }()

	dc := newMockDockerClient()
	exec := newTestExecutor(dc, &mockAckReporter{})
	exec.mu.Lock()
	exec.sandboxes["sbx"] = &sandboxInfo{ContainerID: "ctr", HostPort: 40004, AppName: "app"}
	exec.mu.Unlock()

	exec.RestartSandbox("sbx")
	time.Sleep(80 * time.Millisecond) // let burst 1 fire
	exec.RestartSandbox("sbx")
	time.Sleep(80 * time.Millisecond) // let burst 2 fire

	dc.mu.Lock()
	defer dc.mu.Unlock()
	if len(dc.restartCalls) != 2 {
		t.Errorf("expected 2 restarts (one per burst), got %d: %v", len(dc.restartCalls), dc.restartCalls)
	}
}

func TestExecuteGetLogs(t *testing.T) {
	dc := newMockDockerClient()
	ack := &mockAckReporter{}
	exec := newTestExecutor(dc, ack)

	payload := map[string]interface{}{
		"container_id": "container-logs",
		"tail":         100,
		"follow":       false,
	}

	cmd := makeCommand("get_logs", "sandbox-logs", payload)
	err := exec.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	dc.mu.Lock()
	if len(dc.logCalls) != 1 || dc.logCalls[0] != "container-logs" {
		t.Errorf("expected GetLogs(container-logs), got %v", dc.logCalls)
	}
	dc.mu.Unlock()

	a := ack.lastAck()
	if a.status != "success" {
		t.Errorf("expected ack status=success, got %s", a.status)
	}

	// Verify logs in result
	resultMap, ok := a.result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result to be map, got %T", a.result)
	}
	logs, ok := resultMap["logs"].(string)
	if !ok || logs == "" {
		t.Error("expected non-empty logs in result")
	}
}

func TestExecutePrune(t *testing.T) {
	dc := newMockDockerClient()
	ack := &mockAckReporter{}
	exec := newTestExecutor(dc, ack)

	cmd := makeCommand("prune", "", nil)
	err := exec.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	a := ack.lastAck()
	if a.status != "success" {
		t.Errorf("expected ack status=success, got %s", a.status)
	}
}

func TestExecuteExpiredCommand(t *testing.T) {
	dc := newMockDockerClient()
	ack := &mockAckReporter{}
	exec := newTestExecutor(dc, ack)

	cmd := controlclient.Command{
		ID:             "cmd-expired",
		Type:           "start_sandbox",
		SandboxID:      "sandbox-expired",
		Payload:        json.RawMessage(`{}`),
		IssuedAt:       time.Now().Add(-2 * time.Minute), // 2 minutes ago
		TimeoutSeconds: 60,                               // 60s timeout = expired
	}

	err := exec.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	a := ack.lastAck()
	if a.status != "failure" {
		t.Errorf("expected ack status=failure for expired command, got %s", a.status)
	}
	if !strings.Contains(a.errMsg, "timed out") {
		t.Errorf("expected 'timed out' in error message, got %s", a.errMsg)
	}

	// Verify no Docker calls were made
	dc.mu.Lock()
	if len(dc.createCalls) != 0 {
		t.Error("expected no Docker calls for expired command")
	}
	dc.mu.Unlock()
}

func TestExecuteUnknownCommandType(t *testing.T) {
	dc := newMockDockerClient()
	ack := &mockAckReporter{}
	exec := newTestExecutor(dc, ack)

	cmd := makeCommand("unknown_type", "sandbox-x", nil)
	err := exec.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	a := ack.lastAck()
	if a.status != "failure" {
		t.Errorf("expected ack status=failure for unknown type, got %s", a.status)
	}
	if !strings.Contains(a.errMsg, "unknown command type") {
		t.Errorf("expected 'unknown command type' in error, got %s", a.errMsg)
	}
}

// ── Sleep-not-destroy (2026-06-11) ─────────────────────────────────────

func TestExecuteStopSandboxModeStop_KeepsEverything(t *testing.T) {
	dc := newMockDockerClient()
	ack := &mockAckReporter{}
	exec := newTestExecutor(dc, ack)

	exec.mu.Lock()
	exec.sandboxes["sandbox-sleep"] = &sandboxInfo{
		ContainerID: "container-sleep",
		HostPort:    40003,
		AppName:     "sleep-app",
	}
	exec.mu.Unlock()
	exec.ports.AllocateSpecific(40003)

	cmd := makeCommand("stop_sandbox", "sandbox-sleep", map[string]interface{}{
		"container_id": "container-sleep",
		"mode":         "stop",
	})
	if err := exec.Execute(context.Background(), cmd); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	dc.mu.Lock()
	if len(dc.stopCalls) != 1 || dc.stopCalls[0] != "container-sleep" {
		t.Errorf("expected StopContainer(container-sleep), got %v", dc.stopCalls)
	}
	if len(dc.removeCalls) != 0 {
		t.Errorf("mode=stop must NOT remove the container, got removeCalls=%v", dc.removeCalls)
	}
	dc.mu.Unlock()

	if a := ack.lastAck(); a.status != "success" {
		t.Errorf("expected ack status=success, got %s", a.status)
	}

	// Entry kept for wake.
	exec.mu.RLock()
	info, ok := exec.sandboxes["sandbox-sleep"]
	exec.mu.RUnlock()
	if !ok || info.HostPort != 40003 {
		t.Error("mode=stop must keep the in-memory entry (port reservation included)")
	}
	// Port still reserved: re-reserving it must fail.
	if err := exec.ports.AllocateSpecific(40003); err == nil {
		t.Error("mode=stop must keep the host port reserved")
	}
}

func TestExecuteStopSandboxNoMode_StillDestroys(t *testing.T) {
	dc := newMockDockerClient()
	ack := &mockAckReporter{}
	exec := newTestExecutor(dc, ack)

	exec.mu.Lock()
	exec.sandboxes["sandbox-old"] = &sandboxInfo{
		ContainerID: "container-old",
		HostPort:    40004,
		AppName:     "old-app",
	}
	exec.mu.Unlock()
	exec.ports.AllocateSpecific(40004)

	// Payload WITHOUT mode — a pre-field control plane.
	cmd := makeCommand("stop_sandbox", "sandbox-old", map[string]interface{}{
		"container_id": "container-old",
	})
	if err := exec.Execute(context.Background(), cmd); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	dc.mu.Lock()
	if len(dc.removeCalls) != 1 {
		t.Errorf("absent mode must keep destroy semantics, got removeCalls=%v", dc.removeCalls)
	}
	dc.mu.Unlock()
}

func TestExecuteStartSandbox_WakesStoppedContainer(t *testing.T) {
	dc := newMockDockerClient()
	dc.listContainersFn = func(ctx context.Context) ([]docker.ContainerSnapshot, error) {
		return []docker.ContainerSnapshot{
			{AppName: "wake-app", State: "stopped", HostPort: 40005, ContainerID: "container-wake"},
		}, nil
	}
	ack := &mockAckReporter{}
	exec := newTestExecutor(dc, ack)

	cmd := makeCommand("start_sandbox", "sandbox-wake", map[string]interface{}{
		"app_name": "wake-app",
		"image":    "appx/sandbox:v12",
		"resources": map[string]interface{}{
			"cpu_cores": 0.5,
			"memory_mb": 512,
		},
	})
	if err := exec.Execute(context.Background(), cmd); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	dc.mu.Lock()
	if len(dc.startCalls) != 1 || dc.startCalls[0] != "container-wake" {
		t.Errorf("expected StartContainer(container-wake), got %v", dc.startCalls)
	}
	if len(dc.createCalls) != 0 {
		t.Errorf("wake must reuse the container, not create — got createCalls=%v", dc.createCalls)
	}
	dc.mu.Unlock()

	a := ack.lastAck()
	if a.status != "success" {
		t.Fatalf("expected ack status=success, got %s", a.status)
	}
	result, ok := a.result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map ack result, got %T", a.result)
	}
	if result["container_id"] != "container-wake" {
		t.Errorf("ack must carry the reused container_id, got %v", result["container_id"])
	}
	if port, ok := result["host_port"].(int); ok && port != 40005 {
		t.Errorf("ack must carry the reused host_port, got %d", port)
	}

	// Map adopted.
	exec.mu.RLock()
	info, ok := exec.sandboxes["sandbox-wake"]
	exec.mu.RUnlock()
	if !ok || info.ContainerID != "container-wake" || info.HostPort != 40005 {
		t.Errorf("wake must adopt the container into the map, got %+v", info)
	}
}

func TestExecuteStartSandbox_FallsBackToCreateWhenStartFails(t *testing.T) {
	dc := newMockDockerClient()
	dc.listContainersFn = func(ctx context.Context) ([]docker.ContainerSnapshot, error) {
		return []docker.ContainerSnapshot{
			{AppName: "broken-app", State: "stopped", HostPort: 40006, ContainerID: "container-broken"},
		}, nil
	}
	dc.startContainerFn = func(ctx context.Context, containerID string) error {
		return errors.New("dead container")
	}
	ack := &mockAckReporter{}
	exec := newTestExecutor(dc, ack)

	cmd := makeCommand("start_sandbox", "sandbox-fall", map[string]interface{}{
		"app_name": "broken-app",
		"image":    "appx/sandbox:v12",
		"resources": map[string]interface{}{
			"cpu_cores": 0.5,
			"memory_mb": 512,
		},
	})
	if err := exec.Execute(context.Background(), cmd); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	dc.mu.Lock()
	if len(dc.createCalls) != 1 {
		t.Errorf("failed wake must fall back to CreateContainer, got createCalls=%d", len(dc.createCalls))
	}
	dc.mu.Unlock()
	if a := ack.lastAck(); a.status != "success" {
		t.Errorf("expected fallback create to succeed, got %s", a.status)
	}
}

func TestAdoptBootSnapshot_ReservesPorts(t *testing.T) {
	dc := newMockDockerClient()
	ack := &mockAckReporter{}
	exec := newTestExecutor(dc, ack)

	exec.AdoptBootSnapshot([]docker.ContainerSnapshot{
		{AppName: "a", State: "stopped", HostPort: 40007, ContainerID: "c1"},
		{AppName: "b", State: "running", HostPort: 40008, ContainerID: "c2"},
		{AppName: "no-port", State: "stopped", HostPort: 0, ContainerID: "c3"},
	})

	if err := exec.ports.AllocateSpecific(40007); err == nil {
		t.Error("boot adoption must reserve the slept container port 40007")
	}
	if err := exec.ports.AllocateSpecific(40008); err == nil {
		t.Error("boot adoption must reserve the running container port 40008")
	}
}
