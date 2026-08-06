package natsflow

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	natstest "github.com/nats-io/nats-server/v2/test"
)

// newTestServer starts an embedded, in-process NATS server on a random
// client port for the duration of the test.
func newTestServer(t *testing.T) string {
	t.Helper()
	srv := natstest.RunRandClientPortServer()
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}

// waitConnected polls until the manager reports a connection or the timeout
// elapses.
func waitConnected(t *testing.T, m *Manager, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m.Connected() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("natsflow: manager did not connect within %s", timeout)
}

func TestManager_RunRoundTrip_Success(t *testing.T) {
	url := newTestServer(t)

	m := NewManager(url, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m.Start(ctx)
	t.Cleanup(m.Close)

	waitConnected(t, m, 5*time.Second)

	if err := m.RunRoundTrip(context.Background(), 2*time.Second); err != nil {
		t.Fatalf("RunRoundTrip() error = %v, want nil", err)
	}
}

func TestManager_RunRoundTrip_Concurrent(t *testing.T) {
	url := newTestServer(t)

	m := NewManager(url, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m.Start(ctx)
	t.Cleanup(m.Close)

	waitConnected(t, m, 5*time.Second)

	const n = 10
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			errs <- m.RunRoundTrip(context.Background(), 2*time.Second)
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent RunRoundTrip() error = %v, want nil", err)
		}
	}
}

func TestManager_RunRoundTrip_NotConnected(t *testing.T) {
	m := NewManager("nats://127.0.0.1:0", nil) // never started

	err := m.RunRoundTrip(context.Background(), 100*time.Millisecond)
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("RunRoundTrip() error = %v, want ErrNotConnected", err)
	}
}

func TestManager_ConnectRetriesUntilServerAvailable(t *testing.T) {
	// Reserve a port with a real listener, then free it before any manager
	// connect attempt: the manager should keep retrying against the
	// now-closed port and eventually connect once a server binds it again
	// (task 2.7 — no crash, just retry/backoff).
	srv := natstest.RunRandClientPortServer()
	addr, ok := srv.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected Addr() type %T", srv.Addr())
	}
	port := addr.Port
	url := srv.ClientURL()
	srv.Shutdown()

	m := NewManager(url, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	m.Start(ctx)
	t.Cleanup(m.Close)

	if m.Connected() {
		t.Fatalf("manager reports connected before any server is listening")
	}

	// Bring a server back up on the same port shortly after — well inside
	// the manager's first backoff window.
	opts := natstest.DefaultTestOptions
	opts.Port = port
	srv2 := natstest.RunServer(&opts)
	t.Cleanup(srv2.Shutdown)

	waitConnected(t, m, 5*time.Second)
}
