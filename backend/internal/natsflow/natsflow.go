// Package natsflow owns the backend's NATS connection lifecycle and the
// demo request/reply round trip used by POST /api/demo-trace.
//
// Connection setup is asynchronous with exponential backoff so a
// not-yet-ready NATS server never crash-loops the backend (see Start).
// Once connected, the manager runs two long-lived subscriptions:
//
//   - RequestSubject ("demo.trace.request"): a CONSUMER span per message
//     (via otelnats.Subscribe) that does trivial work and republishes a
//     reply on ReplySubject, carrying the consumed message's trace context
//     forward (a PRODUCER span).
//   - ReplySubject ("demo.trace.reply"): a CONSUMER span per message that
//     resolves the correlation ID back to the goroutine blocked in
//     RunRoundTrip.
package natsflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	nats "github.com/nats-io/nats.go"

	"github.com/akira-core/instrumentation-go/otel-nats/otelnats"
)

const (
	// RequestSubject is the subject the backend publishes the demo NATS
	// request to and subscribes on to simulate a downstream consumer.
	RequestSubject = "demo.trace.request"
	// ReplySubject is the subject the in-process consumer replies on.
	ReplySubject = "demo.trace.reply"

	correlationHeader = "X-Demo-Correlation-Id"

	initialBackoff = 500 * time.Millisecond
	maxBackoff     = 30 * time.Second
)

// ErrNotConnected is returned by RunRoundTrip when no NATS connection is
// currently established (e.g. still retrying in the background).
var ErrNotConnected = errors.New("natsflow: not connected to nats")

// ErrReplyTimeout is returned by RunRoundTrip when the timeout elapses
// before the reply subscriber observes a matching reply.
var ErrReplyTimeout = errors.New("natsflow: timed out waiting for nats reply")

// Manager owns the backend's single NATS connection plus the demo
// publish/subscribe/reply round trip built on top of it.
type Manager struct {
	url    string
	logger *slog.Logger

	conn atomic.Pointer[otelnats.Conn]

	pending sync.Map // correlation ID (string) -> chan struct{}
}

// NewManager builds a Manager for the given NATS URL. It does not connect;
// call Start to begin the async connect-with-retry loop.
func NewManager(url string, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{url: url, logger: logger}
}

// Start begins connecting to NATS in the background with exponential
// backoff, returning immediately. It stops retrying (and stops the
// background subscriptions) when ctx is done.
func (m *Manager) Start(ctx context.Context) {
	go m.connectLoop(ctx)
}

// Connected reports whether a live NATS connection is currently held.
func (m *Manager) Connected() bool {
	return m.conn.Load() != nil
}

// Close closes the underlying NATS connection, if any.
func (m *Manager) Close() {
	if c := m.conn.Load(); c != nil {
		c.Close()
	}
}

func (m *Manager) connectLoop(ctx context.Context) {
	backoff := initialBackoff
	for {
		if ctx.Err() != nil {
			return
		}

		// Deliberately NO otelnats.WithTracingEnabled(...) here. That option
		// pins a connection's tracing state at construction and, per the
		// library's docs, means "no OpenFeature evaluation ever runs for it,
		// no relay change reaches it" — which would silently disable the
		// demo's headline capability: flipping otel-nats-tracing on the relay
		// proxy and watching NATS spans appear/disappear without a restart.
		// Left off, otelnats resolves the flag per operation, falling back to
		// OTEL_NATS_TRACING_ENABLED whenever the relay has no opinion. The
		// env-only kill switch OTEL_INSTRUMENTATION_GO_TRACING_ENABLED must
		// also be truthy or no evaluation happens at all — both are set in
		// deploy/base/backend.yaml.
		conn, err := otelnats.Connect(m.url)
		if err != nil {
			m.logger.WarnContext(ctx, "natsflow: connect failed, retrying",
				"error", err, "url", m.url, "backoff", backoff)
			if !sleepOrDone(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		if err := m.subscribe(conn); err != nil {
			m.logger.ErrorContext(ctx, "natsflow: failed to set up subscriptions, retrying", "error", err)
			conn.Close()
			if !sleepOrDone(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		m.conn.Store(conn)
		m.logger.InfoContext(ctx, "natsflow: connected", "url", m.url)

		go func() {
			<-ctx.Done()
			m.conn.Store(nil)
			conn.Close()
		}()
		return
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func nextBackoff(cur time.Duration) time.Duration {
	next := cur * 2
	if next > maxBackoff {
		return maxBackoff
	}
	return next
}

func (m *Manager) subscribe(conn *otelnats.Conn) error {
	if _, err := conn.Subscribe(RequestSubject, m.handleRequest); err != nil {
		return fmt.Errorf("subscribe %s: %w", RequestSubject, err)
	}
	if _, err := conn.Subscribe(ReplySubject, m.handleReply); err != nil {
		return fmt.Errorf("subscribe %s: %w", ReplySubject, err)
	}
	return nil
}

// handleRequest is the CONSUMER-side handler for RequestSubject: trivial
// simulated work, then a reply publish carrying the extracted trace context
// forward so the whole round trip stays on one trace.
func (m *Manager) handleRequest(msg otelnats.Msg) {
	ctx := msg.Context()
	corrID := msg.Msg.Header.Get(correlationHeader)

	// Trivial simulated work.
	time.Sleep(5 * time.Millisecond)
	m.logger.InfoContext(ctx, "natsflow: processed demo.trace.request", "correlationId", corrID)

	reply := &nats.Msg{
		Subject: ReplySubject,
		Data:    []byte("ok"),
		Header:  nats.Header{},
	}
	if corrID != "" {
		reply.Header.Set(correlationHeader, corrID)
	}

	conn := m.conn.Load()
	if conn == nil {
		m.logger.ErrorContext(ctx, "natsflow: no connection available to send reply")
		return
	}
	if err := conn.PublishMsg(ctx, reply); err != nil {
		m.logger.ErrorContext(ctx, "natsflow: failed to publish reply", "error", err)
	}
}

// handleReply is the CONSUMER-side handler for ReplySubject: it resolves
// the correlation ID back to whichever RunRoundTrip call is waiting on it.
func (m *Manager) handleReply(msg otelnats.Msg) {
	corrID := msg.Msg.Header.Get(correlationHeader)
	if corrID == "" {
		return
	}
	if chAny, ok := m.pending.LoadAndDelete(corrID); ok {
		close(chAny.(chan struct{}))
	}
}

// RunRoundTrip publishes a demo request on RequestSubject (a PRODUCER span
// within ctx's trace) and blocks until the in-process consumer's reply is
// observed on ReplySubject, ctx is canceled, or timeout elapses.
func (m *Manager) RunRoundTrip(ctx context.Context, timeout time.Duration) error {
	conn := m.conn.Load()
	if conn == nil {
		return ErrNotConnected
	}

	corrID, err := newCorrelationID()
	if err != nil {
		return fmt.Errorf("natsflow: generate correlation id: %w", err)
	}

	ch := make(chan struct{})
	m.pending.Store(corrID, ch)
	defer m.pending.Delete(corrID)

	req := &nats.Msg{
		Subject: RequestSubject,
		Data:    []byte("demo-trace-request"),
		Header:  nats.Header{},
	}
	req.Header.Set(correlationHeader, corrID)

	if err := conn.PublishMsg(ctx, req); err != nil {
		return fmt.Errorf("natsflow: publish request: %w", err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ch:
		return nil
	case <-timer.C:
		return ErrReplyTimeout
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newCorrelationID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
