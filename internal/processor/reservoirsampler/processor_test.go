package reservoirsampler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
	"go.uber.org/zap"
)

func TestProcessorBasic(t *testing.T) {
	// Create a temporary directory for the checkpoint file
	tempDir, err := os.MkdirTemp("", "reservoir-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	checkpointPath := filepath.Join(tempDir, "reservoir_test.db")

	// Create a processor with a small reservoir
	cfg := &Config{
		SizeK:              10,
		WindowDuration:     "1s",
		CheckpointPath:     checkpointPath,
		CheckpointInterval: "500ms",
	}

	sink := new(consumertest.TracesSink)
	set := processor.CreateSettings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
		BuildInfo: component.BuildInfo{},
	}

	proc, err := newProcessor(set, cfg, sink)
	require.NoError(t, err)

	// Create a trace with 5 spans
	traces := createTraces(5)

	// Process the traces
	err = proc.ConsumeTraces(context.Background(), traces)
	require.NoError(t, err)

	// Wait for potential async operations
	time.Sleep(100 * time.Millisecond)

	// Flush the reservoir by resetting the window
	p := proc.(*reservoirSamplerProcessor)
	p.resetWindow(time.Now().UnixNano())

	// Wait for flush to complete
	time.Sleep(100 * time.Millisecond)

	// Verify all 5 spans were sampled (since reservoir size is 10)
	assert.GreaterOrEqual(t, len(sink.AllTraces()), 1)
	totalSpans := 0
	for _, traces := range sink.AllTraces() {
		totalSpans += traces.SpanCount()
	}
	assert.Equal(t, 5, totalSpans)
}

func TestReservoirSampling(t *testing.T) {
	// Create a temporary directory for the checkpoint file
	tempDir, err := os.MkdirTemp("", "reservoir-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	checkpointPath := filepath.Join(tempDir, "reservoir_test.db")

	// Create a processor with a tiny reservoir
	cfg := &Config{
		SizeK:              5,
		WindowDuration:     "1s",
		CheckpointPath:     checkpointPath,
		CheckpointInterval: "500ms",
	}

	sink := new(consumertest.TracesSink)
	set := processor.CreateSettings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
		BuildInfo: component.BuildInfo{},
	}

	proc, err := newProcessor(set, cfg, sink)
	require.NoError(t, err)

	// Create a trace with 20 spans (more than reservoir size)
	traces := createTraces(20)

	// Process the traces
	err = proc.ConsumeTraces(context.Background(), traces)
	require.NoError(t, err)

	// Wait for potential async operations
	time.Sleep(100 * time.Millisecond)

	// Flush the reservoir by resetting the window
	p := proc.(*reservoirSamplerProcessor)
	p.resetWindow(time.Now().UnixNano())

	// Wait for flush to complete
	time.Sleep(100 * time.Millisecond)

	// Verify the correct number of spans were sampled
	assert.GreaterOrEqual(t, len(sink.AllTraces()), 1)
	totalSpans := 0
	for _, traces := range sink.AllTraces() {
		totalSpans += traces.SpanCount()
	}
	assert.Equal(t, 5, totalSpans, "Expected exactly 5 spans (reservoir size)")
}

func TestWindowExpiration(t *testing.T) {
	// Create a temporary directory for the checkpoint file
	tempDir, err := os.MkdirTemp("", "reservoir-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	checkpointPath := filepath.Join(tempDir, "reservoir_test.db")

	// Create a processor with a very short window
	cfg := &Config{
		SizeK:              10,
		WindowDuration:     "10ms",
		CheckpointPath:     checkpointPath,
		CheckpointInterval: "500ms",
	}

	sink := new(consumertest.TracesSink)
	set := processor.CreateSettings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
		BuildInfo: component.BuildInfo{},
	}

	proc, err := newProcessor(set, cfg, sink)
	require.NoError(t, err)

	// Create a trace with 5 spans
	traces := createTraces(5)

	// Process the traces
	err = proc.ConsumeTraces(context.Background(), traces)
	require.NoError(t, err)

	// Wait for the window to expire
	time.Sleep(15 * time.Millisecond)

	// Create another trace with 5 more spans
	traces2 := createTraces(5)

	// Process the new traces - this should trigger a window reset
	err = proc.ConsumeTraces(context.Background(), traces2)
	require.NoError(t, err)

	// Wait for flush to complete
	time.Sleep(100 * time.Millisecond)

	// Verify the first batch of spans was flushed
	assert.GreaterOrEqual(t, len(sink.AllTraces()), 1)

	// Force a final flush
	p := proc.(*reservoirSamplerProcessor)
	p.resetWindow(time.Now().UnixNano())

	// Wait for flush to complete
	time.Sleep(100 * time.Millisecond)

	// Now we should have all 10 spans (across 2 flushes)
	totalSpans := 0
	for _, traces := range sink.AllTraces() {
		totalSpans += traces.SpanCount()
	}
	assert.Equal(t, 10, totalSpans)
}

func TestCheckpointing(t *testing.T) {
	// Create a temporary directory for the checkpoint file
	tempDir, err := os.MkdirTemp("", "reservoir-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	checkpointPath := filepath.Join(tempDir, "reservoir_test.db")

	// Create a processor with a short checkpoint interval
	cfg := &Config{
		SizeK:              10,
		WindowDuration:     "10s",
		CheckpointPath:     checkpointPath,
		CheckpointInterval: "50ms",
	}

	sink := new(consumertest.TracesSink)
	set := processor.CreateSettings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
		BuildInfo: component.BuildInfo{},
	}

	proc, err := newProcessor(set, cfg, sink)
	require.NoError(t, err)

	// Create a trace with 5 spans
	traces := createTraces(5)

	// Process the traces
	err = proc.ConsumeTraces(context.Background(), traces)
	require.NoError(t, err)

	// Wait for a checkpoint to occur
	time.Sleep(60 * time.Millisecond)

	// Verify the checkpoint file exists
	_, err = os.Stat(checkpointPath)
	assert.NoError(t, err, "Checkpoint file should exist")

	// Clean up
	p := proc.(*reservoirSamplerProcessor)
	err = p.Shutdown(context.Background())
	require.NoError(t, err)
}

// Helper function to create test traces
func createTraces(spanCount int) ptrace.Traces {
	traces := ptrace.NewTraces()

	for i := 0; i < spanCount; i++ {
		rs := traces.ResourceSpans().AppendEmpty()
		rs.Resource().Attributes().PutStr("service.name", "test-service")

		ss := rs.ScopeSpans().AppendEmpty()
		ss.Scope().SetName("test-scope")

		span := ss.Spans().AppendEmpty()

		// Generate a unique trace ID for each span
		traceID := [16]byte{}
		for j := 0; j < 16; j++ {
			traceID[j] = byte((i*16 + j) % 256)
		}
		span.SetTraceID(pcommon.TraceID(traceID))

		// Generate a unique span ID
		spanID := [8]byte{}
		for j := 0; j < 8; j++ {
			spanID[j] = byte((i*8 + j) % 256)
		}
		span.SetSpanID(pcommon.SpanID(spanID))

		span.SetName(fmt.Sprintf("test-span-%d", i))
		span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now()))
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(time.Millisecond * 100)))

		// Add some attributes
		span.Attributes().PutInt("sequence", int64(i))
		span.Attributes().PutBool("test", true)
	}

	return traces
}
