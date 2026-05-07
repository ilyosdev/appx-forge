package docker

import (
	"context"
	"io"
	"testing"
	"time"
)

// mockClient is a minimal mock that satisfies the Client interface.
// It proves the interface is implementable beyond the real dockerClient.
type mockClient struct{}

func (m *mockClient) CreateContainer(_ context.Context, _ *SandboxSpec) (string, error) {
	return "mock-container-id", nil
}
func (m *mockClient) StartContainer(_ context.Context, _ string) error  { return nil }
func (m *mockClient) StopContainer(_ context.Context, _ string, _ time.Duration) error {
	return nil
}
func (m *mockClient) RemoveContainer(_ context.Context, _ string) error { return nil }
func (m *mockClient) RestartContainer(_ context.Context, _ string, _ time.Duration) error {
	return nil
}
func (m *mockClient) InspectContainer(_ context.Context, _ string) (*ContainerInfo, error) {
	return &ContainerInfo{
		ID:      "mock-id",
		Name:    "forge-test",
		State:   "running",
		Running: true,
	}, nil
}
func (m *mockClient) GetLogs(_ context.Context, _ string, _ int, _ bool) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}
func (m *mockClient) PullImage(_ context.Context, _ string) error { return nil }
func (m *mockClient) EventsStream(_ context.Context, _ time.Time) (<-chan ContainerEvent, <-chan error) {
	return make(chan ContainerEvent), make(chan error)
}
func (m *mockClient) ListContainers(_ context.Context) ([]ContainerSnapshot, error) {
	return []ContainerSnapshot{}, nil
}
func (m *mockClient) Close() error { return nil }

// TestMockClientSatisfiesInterface verifies that the mockClient implements Client.
func TestMockClientSatisfiesInterface(t *testing.T) {
	var c Client = &mockClient{}
	if c == nil {
		t.Fatal("mockClient should not be nil")
	}
}

// TestDockerClientSatisfiesInterface verifies that dockerClient implements Client.
// This is a compile-time check enforced at test time.
func TestDockerClientSatisfiesInterface(t *testing.T) {
	// Compile-time interface check -- does not call NewDockerClient
	// because Docker may not be available in CI.
	var _ Client = (*dockerClient)(nil)
}

// TestSandboxSpecFields verifies that SandboxSpec has all required fields.
func TestSandboxSpecFields(t *testing.T) {
	spec := &SandboxSpec{
		SandboxID:   "sandbox-123",
		AppName:     "my-app",
		Image:       "appx/sandbox:v1",
		HostPort:    43210,
		CPUCores:    0.5,
		MemoryMB:    512,
		Env:         map[string]string{"APP_NAME": "my-app", "PORT": "8081"},
		SandboxDir:  "/var/lib/forge/sandboxes",
		SeccompPath: "/etc/forge/seccomp-default.json",
	}

	if spec.SandboxID != "sandbox-123" {
		t.Errorf("SandboxID = %q, want %q", spec.SandboxID, "sandbox-123")
	}
	if spec.AppName != "my-app" {
		t.Errorf("AppName = %q, want %q", spec.AppName, "my-app")
	}
	if spec.Image != "appx/sandbox:v1" {
		t.Errorf("Image = %q, want %q", spec.Image, "appx/sandbox:v1")
	}
	if spec.HostPort != 43210 {
		t.Errorf("HostPort = %d, want %d", spec.HostPort, 43210)
	}
	if spec.CPUCores != 0.5 {
		t.Errorf("CPUCores = %f, want %f", spec.CPUCores, 0.5)
	}
	if spec.MemoryMB != 512 {
		t.Errorf("MemoryMB = %d, want %d", spec.MemoryMB, 512)
	}
	if spec.SandboxDir != "/var/lib/forge/sandboxes" {
		t.Errorf("SandboxDir = %q, want %q", spec.SandboxDir, "/var/lib/forge/sandboxes")
	}
	if spec.SeccompPath != "/etc/forge/seccomp-default.json" {
		t.Errorf("SeccompPath = %q, want %q", spec.SeccompPath, "/etc/forge/seccomp-default.json")
	}
}

// TestContainerInfoFields verifies that ContainerInfo has all required fields.
func TestContainerInfoFields(t *testing.T) {
	now := time.Now()
	info := &ContainerInfo{
		ID:        "abc123",
		Name:      "forge-test",
		State:     "running",
		Running:   true,
		ExitCode:  0,
		StartedAt: now,
	}

	if info.ID != "abc123" {
		t.Errorf("ID = %q, want %q", info.ID, "abc123")
	}
	if info.Name != "forge-test" {
		t.Errorf("Name = %q, want %q", info.Name, "forge-test")
	}
	if info.State != "running" {
		t.Errorf("State = %q, want %q", info.State, "running")
	}
	if !info.Running {
		t.Error("Running = false, want true")
	}
	if info.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want %d", info.ExitCode, 0)
	}
	if !info.StartedAt.Equal(now) {
		t.Errorf("StartedAt = %v, want %v", info.StartedAt, now)
	}
}

// TestContainerEventFields verifies that ContainerEvent has all required fields.
func TestContainerEventFields(t *testing.T) {
	now := time.Now()
	event := ContainerEvent{
		ContainerID:   "abc123",
		ContainerName: "forge-test",
		Action:        "die",
		ExitCode:      "137",
		Time:          now,
	}

	if event.ContainerID != "abc123" {
		t.Errorf("ContainerID = %q, want %q", event.ContainerID, "abc123")
	}
	if event.ContainerName != "forge-test" {
		t.Errorf("ContainerName = %q, want %q", event.ContainerName, "forge-test")
	}
	if event.Action != "die" {
		t.Errorf("Action = %q, want %q", event.Action, "die")
	}
	if event.ExitCode != "137" {
		t.Errorf("ExitCode = %q, want %q", event.ExitCode, "137")
	}
	if !event.Time.Equal(now) {
		t.Errorf("Time = %v, want %v", event.Time, now)
	}
}
