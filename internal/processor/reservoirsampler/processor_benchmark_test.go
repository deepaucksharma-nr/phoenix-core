// Copyright The NR Phoenix Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package reservoirsampler

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/processor"
	"go.uber.org/zap"
)

// BenchmarkStandardSampling benchmarks standard (non-trace-aware) sampling
func BenchmarkStandardSampling(b *testing.B) {
	// Create a temporary directory for the checkpoint file
	tempDir, err := os.MkdirTemp("", "benchmark-standard-*")
	require.NoError(b, err)
	defer os.RemoveAll(tempDir)

	checkpointPath := filepath.Join(tempDir, "benchmark.db")

	// Create processor configuration with standard sampling
	cfg := &Config{
		SizeK:              1000,
		WindowDuration:     "60s",
		CheckpointPath:     checkpointPath,
		CheckpointInterval: "10s",
		TraceAware:         false,
	}

	sink := new(consumertest.TracesSink)
	set := processor.CreateSettings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
		BuildInfo: component.BuildInfo{},
	}

	// Create the processor
	proc, err := newProcessor(set, cfg, sink)
	require.NoError(b, err)

	// Ensure processor is started
	err = proc.Start(context.Background(), nil)
	require.NoError(b, err)
	defer proc.Shutdown(context.Background())

	// Reset benchmark timer before the main test loop
	b.ResetTimer()

	// Run the benchmark
	for i := 0; i < b.N; i++ {
		// Create a batch of traces - 100 traces with 5 spans each
		traces := createTraceWithMultipleSpans(i, 5)

		// Process the traces
		err = proc.ConsumeTraces(context.Background(), traces)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTraceAwareSampling benchmarks trace-aware sampling
func BenchmarkTraceAwareSampling(b *testing.B) {
	// Create a temporary directory for the checkpoint file
	tempDir, err := os.MkdirTemp("", "benchmark-traceaware-*")
	require.NoError(b, err)
	defer os.RemoveAll(tempDir)

	checkpointPath := filepath.Join(tempDir, "benchmark.db")

	// Create processor configuration with trace-aware sampling
	cfg := &Config{
		SizeK:              1000,
		WindowDuration:     "60s",
		CheckpointPath:     checkpointPath,
		CheckpointInterval: "10s",
		TraceAware:         true,
		TraceTimeout:       "1s",
	}

	sink := new(consumertest.TracesSink)
	set := processor.CreateSettings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
		BuildInfo: component.BuildInfo{},
	}

	// Create the processor
	proc, err := newProcessor(set, cfg, sink)
	require.NoError(b, err)

	// Ensure processor is started
	err = proc.Start(context.Background(), nil)
	require.NoError(b, err)
	defer proc.Shutdown(context.Background())

	// Reset benchmark timer before the main test loop
	b.ResetTimer()

	// Run the benchmark
	for i := 0; i < b.N; i++ {
		// Create a batch of traces - 100 traces with 5 spans each
		traces := createTraceWithMultipleSpans(i, 5)

		// Process the traces
		err = proc.ConsumeTraces(context.Background(), traces)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTraceAwareSamplingWithTimeouts benchmarks trace-aware sampling with timeouts
func BenchmarkTraceAwareSamplingWithTimeouts(b *testing.B) {
	// Create a temporary directory for the checkpoint file
	tempDir, err := os.MkdirTemp("", "benchmark-traceaware-timeouts-*")
	require.NoError(b, err)
	defer os.RemoveAll(tempDir)

	checkpointPath := filepath.Join(tempDir, "benchmark.db")

	// Create processor configuration with trace-aware sampling and very short timeouts
	cfg := &Config{
		SizeK:              1000,
		WindowDuration:     "60s",
		CheckpointPath:     checkpointPath,
		CheckpointInterval: "10s",
		TraceAware:         true,
		TraceTimeout:       "10ms", // Very short timeout to trigger timeouts
	}

	sink := new(consumertest.TracesSink)
	set := processor.CreateSettings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
		BuildInfo: component.BuildInfo{},
	}

	// Create the processor
	proc, err := newProcessor(set, cfg, sink)
	require.NoError(b, err)

	// Ensure processor is started
	err = proc.Start(context.Background(), nil)
	require.NoError(b, err)
	defer proc.Shutdown(context.Background())

	// Reset benchmark timer before the main test loop
	b.ResetTimer()

	// Run the benchmark
	for i := 0; i < b.N; i++ {
		// Create a batch of traces - all orphaned (no root spans)
		// so they will time out eventually
		traces := createOrphanedTrace(i, 5)

		// Process the traces
		err = proc.ConsumeTraces(context.Background(), traces)
		if err != nil {
			b.Fatal(err)
		}

		// Every 10 iterations, wait for timeouts to occur
		if i > 0 && i%10 == 0 {
			time.Sleep(15 * time.Millisecond) // Sleep just enough for timeouts
		}
	}
}

// Helper to create a trace with only orphaned spans (no root span)
func createOrphanedTrace(traceIndex, spanCount int) ptrace.Traces {
	traces := ptrace.NewTraces()

	// Generate a unique trace ID for this trace
	traceID := [16]byte{}
	for j := 0; j < 16; j++ {
		traceID[j] = byte((traceIndex*16 + j) % 256)
	}

	// Create a resource span
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "test-service")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("test-scope")

	// Arbitrary parent span ID that doesn't exist
	nonExistentParentID := [8]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

	// Create orphaned spans (all with parent IDs that don't exist)
	for i := 0; i < spanCount; i++ {
		childSpan := ss.Spans().AppendEmpty()
		childSpan.SetTraceID(pcommon.TraceID(traceID))
		
		// Generate a unique span ID
		spanID := [8]byte{}
		for j := 0; j < 8; j++ {
			spanID[j] = byte((i*8 + j) % 256)
		}
		childSpan.SetSpanID(pcommon.SpanID(spanID))
		
		// Set parent span ID to a non-existent span
		childSpan.SetParentSpanID(pcommon.SpanID(nonExistentParentID))
		
		childSpan.SetName(fmt.Sprintf("orphan-span-%d", i))
		childSpan.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now()))
		childSpan.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(time.Millisecond * 100)))
		
		// Add some attributes
		childSpan.Attributes().PutInt("orphan_index", int64(i))
	}

	return traces
}