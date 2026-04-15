package docker

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	dockerclient "github.com/moby/moby/client"
)

// capturedCreate holds the arguments captured from a ContainerCreate call.
type capturedCreate struct {
	opts dockerclient.ContainerCreateOptions
}

// mockDockerSDK is a mock for the raw Docker SDK client that captures
// the ContainerCreate arguments for test assertions.
type mockDockerSDK struct {
	createCalled bool
	captured     capturedCreate
	startCalled  bool
}

func (m *mockDockerSDK) ContainerCreate(_ context.Context, opts dockerclient.ContainerCreateOptions) (dockerclient.ContainerCreateResult, error) {
	m.createCalled = true
	m.captured = capturedCreate{opts: opts}
	return dockerclient.ContainerCreateResult{ID: "test-container-id"}, nil
}

func (m *mockDockerSDK) ContainerStart(_ context.Context, _ string, _ dockerclient.ContainerStartOptions) (dockerclient.ContainerStartResult, error) {
	m.startCalled = true
	return dockerclient.ContainerStartResult{}, nil
}

// newTestClient creates a dockerClient with a mock SDK client for testing.
func newTestClient(mock *mockDockerSDK) *dockerClient {
	return &dockerClient{rawClient: mock}
}

// defaultSpec returns a SandboxSpec with all fields set to standard test values.
func defaultSpec(t *testing.T) *SandboxSpec {
	t.Helper()
	return &SandboxSpec{
		SandboxID:   "sandbox-test-123",
		AppName:     "my-cool-app",
		Image:       "appx/sandbox:v1",
		HostPort:    43210,
		CPUCores:    0.5,
		MemoryMB:    512,
		Env:         map[string]string{"APP_NAME": "my-cool-app", "PORT": "8081"},
		SandboxDir:  t.TempDir(),
		SeccompPath: "/etc/forge/seccomp-default.json",
	}
}

// TestCreateContainerConfig verifies container.Config fields (Image, Env, ExposedPorts).
func TestCreateContainerConfig(t *testing.T) {
	mock := &mockDockerSDK{}
	client := newTestClient(mock)
	spec := defaultSpec(t)

	_, err := client.CreateContainer(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateContainer failed: %v", err)
	}

	cfg := mock.captured.opts.Config
	if cfg == nil {
		t.Fatal("Config is nil")
	}

	// Image
	if cfg.Image != "appx/sandbox:v1" {
		t.Errorf("Config.Image = %q, want %q", cfg.Image, "appx/sandbox:v1")
	}

	// Env should contain our key-value pairs as KEY=VALUE strings
	envMap := make(map[string]string)
	for _, e := range cfg.Env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}
	if envMap["APP_NAME"] != "my-cool-app" {
		t.Errorf("Env APP_NAME = %q, want %q", envMap["APP_NAME"], "my-cool-app")
	}
	if envMap["PORT"] != "8081" {
		t.Errorf("Env PORT = %q, want %q", envMap["PORT"], "8081")
	}

	// ExposedPorts should include 8081/tcp
	port := network.MustParsePort("8081/tcp")
	if _, ok := cfg.ExposedPorts[port]; !ok {
		t.Errorf("ExposedPorts missing 8081/tcp, got %v", cfg.ExposedPorts)
	}
}

// TestCreateContainerPortBindings verifies HostConfig port bindings.
func TestCreateContainerPortBindings(t *testing.T) {
	mock := &mockDockerSDK{}
	client := newTestClient(mock)
	spec := defaultSpec(t)

	_, err := client.CreateContainer(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateContainer failed: %v", err)
	}

	hc := mock.captured.opts.HostConfig
	if hc == nil {
		t.Fatal("HostConfig is nil")
	}

	port := network.MustParsePort("8081/tcp")
	bindings, ok := hc.PortBindings[port]
	if !ok {
		t.Fatalf("PortBindings missing 8081/tcp, got %v", hc.PortBindings)
	}
	if len(bindings) != 1 {
		t.Fatalf("PortBindings[8081/tcp] has %d bindings, want 1", len(bindings))
	}
	expectedIP := netip.MustParseAddr("0.0.0.0")
	if bindings[0].HostIP != expectedIP {
		t.Errorf("HostIP = %v, want %v", bindings[0].HostIP, expectedIP)
	}
	if bindings[0].HostPort != "43210" {
		t.Errorf("HostPort = %q, want %q", bindings[0].HostPort, "43210")
	}
}

// TestCreateContainerResourceLimits verifies memory and CPU limits.
func TestCreateContainerResourceLimits(t *testing.T) {
	mock := &mockDockerSDK{}
	client := newTestClient(mock)
	spec := defaultSpec(t)

	_, err := client.CreateContainer(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateContainer failed: %v", err)
	}

	hc := mock.captured.opts.HostConfig
	if hc == nil {
		t.Fatal("HostConfig is nil")
	}

	// Memory: 512MB = 512 * 1024 * 1024 = 536870912 bytes
	expectedMemory := int64(512 * 1024 * 1024)
	if hc.Resources.Memory != expectedMemory {
		t.Errorf("Resources.Memory = %d, want %d", hc.Resources.Memory, expectedMemory)
	}

	// CPU: 0.5 cores = 0.5 * 1e9 = 500000000 NanoCPUs
	expectedCPU := int64(0.5 * 1e9)
	if hc.Resources.NanoCPUs != expectedCPU {
		t.Errorf("Resources.NanoCPUs = %d, want %d", hc.Resources.NanoCPUs, expectedCPU)
	}
}

// TestCreateContainerSecurityOpt verifies seccomp and no-new-privileges.
func TestCreateContainerSecurityOpt(t *testing.T) {
	mock := &mockDockerSDK{}
	client := newTestClient(mock)
	spec := defaultSpec(t)

	_, err := client.CreateContainer(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateContainer failed: %v", err)
	}

	hc := mock.captured.opts.HostConfig
	if hc == nil {
		t.Fatal("HostConfig is nil")
	}

	secOpts := hc.SecurityOpt
	foundSeccomp := false
	foundNoNewPriv := false
	for _, opt := range secOpts {
		if opt == "seccomp=/etc/forge/seccomp-default.json" {
			foundSeccomp = true
		}
		if opt == "no-new-privileges:true" {
			foundNoNewPriv = true
		}
	}

	if !foundSeccomp {
		t.Errorf("SecurityOpt missing seccomp profile, got %v", secOpts)
	}
	if !foundNoNewPriv {
		t.Errorf("SecurityOpt missing no-new-privileges:true, got %v", secOpts)
	}
}

// TestCreateContainerCapabilities verifies ALL caps dropped and only required caps added.
func TestCreateContainerCapabilities(t *testing.T) {
	mock := &mockDockerSDK{}
	client := newTestClient(mock)
	spec := defaultSpec(t)

	_, err := client.CreateContainer(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateContainer failed: %v", err)
	}

	hc := mock.captured.opts.HostConfig
	if hc == nil {
		t.Fatal("HostConfig is nil")
	}

	// CapDrop should have exactly ["ALL"]
	if len(hc.CapDrop) != 1 || hc.CapDrop[0] != "ALL" {
		t.Errorf("CapDrop = %v, want [ALL]", hc.CapDrop)
	}

	// CapAdd should have exactly CHOWN, SETUID, SETGID
	expectedCaps := map[string]bool{"CHOWN": true, "SETUID": true, "SETGID": true}
	if len(hc.CapAdd) != 3 {
		t.Fatalf("CapAdd has %d entries, want 3: %v", len(hc.CapAdd), hc.CapAdd)
	}
	for _, cap := range hc.CapAdd {
		if !expectedCaps[cap] {
			t.Errorf("unexpected cap in CapAdd: %q", cap)
		}
	}
}

// TestCreateContainerPidsLimit verifies PID limit is set to 256.
func TestCreateContainerPidsLimit(t *testing.T) {
	mock := &mockDockerSDK{}
	client := newTestClient(mock)
	spec := defaultSpec(t)

	_, err := client.CreateContainer(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateContainer failed: %v", err)
	}

	hc := mock.captured.opts.HostConfig
	if hc == nil {
		t.Fatal("HostConfig is nil")
	}

	if hc.Resources.PidsLimit == nil {
		t.Fatal("PidsLimit is nil, want 256")
	}
	if *hc.Resources.PidsLimit != 256 {
		t.Errorf("PidsLimit = %d, want 256", *hc.Resources.PidsLimit)
	}
}

// TestCreateContainerName verifies the container name follows "forge-{app_name}" convention.
func TestCreateContainerName(t *testing.T) {
	mock := &mockDockerSDK{}
	client := newTestClient(mock)
	spec := defaultSpec(t)

	_, err := client.CreateContainer(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateContainer failed: %v", err)
	}

	name := mock.captured.opts.Name
	expected := "forge-my-cool-app"
	if name != expected {
		t.Errorf("container Name = %q, want %q", name, expected)
	}
}

// TestCreateContainerBindMount verifies the bind mount path.
func TestCreateContainerBindMount(t *testing.T) {
	mock := &mockDockerSDK{}
	client := newTestClient(mock)
	spec := defaultSpec(t)

	_, err := client.CreateContainer(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateContainer failed: %v", err)
	}

	hc := mock.captured.opts.HostConfig
	if hc == nil {
		t.Fatal("HostConfig is nil")
	}

	expectedBind := filepath.Join(spec.SandboxDir, "my-cool-app", "code") + ":/app/code"
	if len(hc.Binds) != 1 {
		t.Fatalf("Binds has %d entries, want 1: %v", len(hc.Binds), hc.Binds)
	}
	if hc.Binds[0] != expectedBind {
		t.Errorf("Bind = %q, want %q", hc.Binds[0], expectedBind)
	}
}

// TestPrepareBindMountCreatesDirectory verifies that PrepareBindMount creates
// the directory with correct permissions.
func TestPrepareBindMountCreatesDirectory(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Skipping on non-unix platform")
	}

	tmpDir := t.TempDir()
	codePath, err := PrepareBindMount(tmpDir, "test-app")
	if err != nil {
		t.Fatalf("PrepareBindMount failed: %v", err)
	}

	expected := filepath.Join(tmpDir, "test-app", "code")
	if codePath != expected {
		t.Errorf("codePath = %q, want %q", codePath, expected)
	}

	// Verify directory exists
	info, err := os.Stat(codePath)
	if err != nil {
		t.Fatalf("stat codePath: %v", err)
	}
	if !info.IsDir() {
		t.Error("codePath is not a directory")
	}

	// Verify permissions (0755)
	perm := info.Mode().Perm()
	if perm != 0755 {
		t.Errorf("codePath permissions = %o, want 0755", perm)
	}
}

// TestPrepareBindMountRejectsPathTraversal verifies that ".." in appName is rejected.
func TestPrepareBindMountRejectsPathTraversal(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name    string
		appName string
	}{
		{"double dot", "../escape"},
		{"embedded double dot", "foo/../bar"},
		{"slash", "foo/bar"},
		{"absolute path", "/etc/passwd"},
		{"dot-dot only", ".."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := PrepareBindMount(tmpDir, tt.appName)
			if err == nil {
				t.Errorf("PrepareBindMount(%q) should have returned error for path traversal", tt.appName)
			}
		})
	}
}

// TestCreateContainerCallsStartAfterCreate verifies that ContainerStart is called
// after ContainerCreate.
func TestCreateContainerCallsStartAfterCreate(t *testing.T) {
	mock := &mockDockerSDK{}
	client := newTestClient(mock)
	spec := defaultSpec(t)

	_, err := client.CreateContainer(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateContainer failed: %v", err)
	}

	if !mock.createCalled {
		t.Error("ContainerCreate was not called")
	}
	if !mock.startCalled {
		t.Error("ContainerStart was not called after ContainerCreate")
	}
}

// TestCreateContainerReadonlyRootfs verifies ReadonlyRootfs is false.
func TestCreateContainerReadonlyRootfs(t *testing.T) {
	mock := &mockDockerSDK{}
	client := newTestClient(mock)
	spec := defaultSpec(t)

	_, err := client.CreateContainer(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateContainer failed: %v", err)
	}

	hc := mock.captured.opts.HostConfig
	if hc == nil {
		t.Fatal("HostConfig is nil")
	}

	if hc.ReadonlyRootfs {
		t.Error("ReadonlyRootfs = true, want false (Metro writes cache)")
	}
}

// TestCreateContainerReturnsID verifies the container ID is returned correctly.
func TestCreateContainerReturnsID(t *testing.T) {
	mock := &mockDockerSDK{}
	client := newTestClient(mock)
	spec := defaultSpec(t)

	containerID, err := client.CreateContainer(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateContainer failed: %v", err)
	}

	if containerID != "test-container-id" {
		t.Errorf("containerID = %q, want %q", containerID, "test-container-id")
	}
}

// --- Helper to check if HostConfig has a specific security opt ---

func hasSecurityOpt(hc *container.HostConfig, opt string) bool {
	for _, o := range hc.SecurityOpt {
		if o == opt {
			return true
		}
	}
	return false
}
