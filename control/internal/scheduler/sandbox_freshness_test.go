package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeAgent struct {
	exists bool
	state  string
	err    error
	called int
}

func (f *fakeAgent) ContainerExists(ctx context.Context, name string) (bool, string, error) {
	f.called++
	return f.exists, f.state, f.err
}

type fakeFreshStore struct {
	row             *SandboxRow
	verifiedAt      time.Time
	markedVerified  []verifiedCall
	markedDestroyed bool
	destroyedReason string
}

func (f *fakeFreshStore) GetSandboxByName(ctx context.Context, name string) (*SandboxRow, time.Time, error) {
	if f.row == nil {
		return nil, time.Time{}, errors.New("not found")
	}
	return f.row, f.verifiedAt, nil
}

func (f *fakeFreshStore) MarkSandboxVerified(ctx context.Context, appName, state string) error {
	f.markedVerified = append(f.markedVerified, verifiedCall{appName, state})
	return nil
}

func (f *fakeFreshStore) MarkSandboxDestroyed(ctx context.Context, appName, reason string) error {
	f.markedDestroyed = true
	f.destroyedReason = reason
	return nil
}

func TestFreshness_FreshRow_NoAgentCall(t *testing.T) {
	store := &fakeFreshStore{
		row:        &SandboxRow{AppName: "pool-X", State: "running"},
		verifiedAt: time.Now().Add(-5 * time.Second),
	}
	agent := &fakeAgent{exists: true, state: "running"}
	svc := NewSandboxFreshnessService(store, agent, 10*time.Second, nil)

	row, _, err := svc.GetSandbox(context.Background(), "pool-X", false)
	if err != nil {
		t.Fatal(err)
	}
	if row.AppName != "pool-X" {
		t.Errorf("wrong row: %+v", row)
	}
	if agent.called != 0 {
		t.Errorf("agent should not have been called for fresh row, called=%d", agent.called)
	}
}

func TestFreshness_StaleRow_TriggersAgentQuery(t *testing.T) {
	store := &fakeFreshStore{
		row:        &SandboxRow{AppName: "pool-X", State: "running"},
		verifiedAt: time.Now().Add(-30 * time.Second),
	}
	agent := &fakeAgent{exists: true, state: "running"}
	svc := NewSandboxFreshnessService(store, agent, 10*time.Second, nil)

	_, _, err := svc.GetSandbox(context.Background(), "pool-X", false)
	if err != nil {
		t.Fatal(err)
	}
	if agent.called != 1 {
		t.Errorf("agent should have been called, called=%d", agent.called)
	}
	if len(store.markedVerified) != 1 || store.markedVerified[0].AppName != "pool-X" {
		t.Errorf("expected verified pool-X, got %+v", store.markedVerified)
	}
}

func TestFreshness_AgentSays404_MarksDestroyedAndReturns404(t *testing.T) {
	store := &fakeFreshStore{
		row:        &SandboxRow{AppName: "pool-X", State: "running"},
		verifiedAt: time.Now().Add(-30 * time.Second),
	}
	agent := &fakeAgent{exists: false}
	svc := NewSandboxFreshnessService(store, agent, 10*time.Second, nil)

	_, _, err := svc.GetSandbox(context.Background(), "pool-X", true)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrSandboxNotFound) {
		t.Errorf("expected ErrSandboxNotFound, got %v", err)
	}
	if !store.markedDestroyed {
		t.Errorf("expected store.MarkSandboxDestroyed called")
	}
	if store.destroyedReason != "agent_lost_at_freshness_check" {
		t.Errorf("unexpected destroy reason: %q", store.destroyedReason)
	}
}

func TestFreshness_ForceRefresh_AlwaysCallsAgent(t *testing.T) {
	store := &fakeFreshStore{
		row:        &SandboxRow{AppName: "pool-X", State: "running"},
		verifiedAt: time.Now().Add(-1 * time.Second),
	}
	agent := &fakeAgent{exists: true, state: "running"}
	svc := NewSandboxFreshnessService(store, agent, 10*time.Second, nil)

	_, _, err := svc.GetSandbox(context.Background(), "pool-X", true)
	if err != nil {
		t.Fatal(err)
	}
	if agent.called != 1 {
		t.Errorf("force_refresh should have called agent, called=%d", agent.called)
	}
}

func TestFreshness_AgentUnreachable_ReturnsCached(t *testing.T) {
	cachedVerifiedAt := time.Now().Add(-30 * time.Second)
	store := &fakeFreshStore{
		row:        &SandboxRow{AppName: "pool-X", State: "running"},
		verifiedAt: cachedVerifiedAt,
	}
	agent := &fakeAgent{err: errors.New("connection refused")}
	svc := NewSandboxFreshnessService(store, agent, 10*time.Second, nil)

	row, vat, err := svc.GetSandbox(context.Background(), "pool-X", false)
	if err != nil {
		t.Fatalf("agent unreachable should NOT propagate, got err=%v", err)
	}
	if row.AppName != "pool-X" {
		t.Errorf("expected cached row returned, got %+v", row)
	}
	if !vat.Equal(cachedVerifiedAt) {
		t.Errorf("expected cached verified_at, got %v", vat)
	}
	if store.markedDestroyed {
		t.Errorf("must NOT mark destroyed on agent unreachable (would cause spurious destroys during agent flaps)")
	}
}
