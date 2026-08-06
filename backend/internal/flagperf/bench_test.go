package flagperf

import (
	"context"
	"os"
	"testing"

	natstest "github.com/nats-io/nats-server/v2/test"

	otelnats "github.com/akira-core/instrumentation-go/otel-nats/otelnats"
)

// The Go benchmark entry points exist for the two things the evidence harness
// cannot produce: allocs/op and B/op from -benchmem, and a distribution
// benchstat can test for significance across -count runs.
//
// ONE MODE PER PROCESS, for the same reason the evidence harness spawns a
// worker per mode: otel-flags latches its provider install for the life of the
// process, so a second sub-benchmark in the same binary would inherit the
// first's decision. The mode is therefore selected by FLAGPERF_MODE and the
// benchmark skips without it, rather than ranging over the modes internally.
//
//	for m in no_flag_no_env env_on_no_flag flag_off_memprovider \
//	         flag_on_memprovider flag_off_relay flag_on_relay; do
//	  FLAGPERF_MODE=$m go test ./internal/flagperf/ -run '^$' \
//	    -bench 'BenchmarkPublish$' -benchmem -count=10 | tee "bench-$m.txt"
//	done
//	benchstat bench-no_flag_no_env.txt bench-flag_on_relay.txt

// benchConn applies the FLAGPERF_MODE configuration and returns a warmed Conn.
func benchConn(b *testing.B) *otelnats.Conn {
	b.Helper()

	id := os.Getenv(envModeID)
	if id == "" {
		b.Skipf("set %s=<mode id> and run one mode per process; see bench_test.go", envModeID)
	}
	mode, ok := modeByID(id)
	if !ok {
		b.Fatalf("unknown mode %q", id)
	}

	srv := natstest.RunRandClientPortServer()
	b.Cleanup(srv.Shutdown)

	tp := newTracerProvider()
	b.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	conn := setupConn(b, srv.ClientURL(), mode)

	ctx := context.Background()
	for i := 0; i < warmupOps; i++ {
		_ = conn.Publish(ctx, subject, payload)
	}
	_ = conn.NatsConn().Flush()
	return conn
}

// BenchmarkPublish reports ns/op, B/op and allocs/op for one Publish.
//
// allocs/op is the number otel-flags's own documentation puts at "roughly 2 µs
// and 7 allocations per call" for a single evaluation, and a Publish makes two
// of them. Having it measured here is what lets the report check that claim
// rather than repeat it.
func BenchmarkPublish(b *testing.B) {
	conn := benchConn(b)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = conn.Publish(ctx, subject, payload)
	}
}

// BenchmarkPublishParallel is the same operation under b.RunParallel, so
// benchstat can compare the per-op cost of a contended gate against an
// uncontended one. GOMAXPROCS sets the parallelism; -cpu varies it.
func BenchmarkPublishParallel(b *testing.B) {
	conn := benchConn(b)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		for pb.Next() {
			_ = conn.Publish(ctx, subject, payload)
		}
	})
}
