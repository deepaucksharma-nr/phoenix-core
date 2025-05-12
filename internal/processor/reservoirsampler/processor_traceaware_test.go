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

func TestTraceAwareProcessor(t *testing.T) {
	// Create a temporary directory for the checkpoint file
	tempDir, err := os.MkdirTemp("", "reservoir-trace-aware-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	checkpointPath := filepath.Join(tempDir, "reservoir_test.db")

	// Create a processor with trace-aware sampling enabled
	cfg := &Config{
		SizeK:              10,
		WindowDuration:     "1s",
		CheckpointPath:     checkpointPath,
		CheckpointInterval: "500ms",
		TraceAware:         true,
		TraceTimeout:       "100ms",
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

	// Ensure processor is started
	err = proc.Start(context.Background(), nil)
	require.NoError(t, err)
	defer proc.Shutdown(context.Background())

	// Create a trace with parent and child spans
	traces := createTraceWithParentChild()

	// Process the traces
	err = proc.ConsumeTraces(context.Background(), traces)
	require.NoError(t, err)

	// Wait for the trace timeout to occur
	time.Sleep(150 * time.Millisecond)

	// Verify traces were processed (should have been released by timeout)
	assert.GreaterOrEqual(t, len(sink.AllTraces()), 1, "Expected at least one trace to be processed")

	// Create a second trace with only a root span
	traces2 := createTraceWithRootSpan()

	// Process the second trace
	err = proc.ConsumeTraces(context.Background(), traces2)
	require.NoError(t, err)

	// Wait for potential async operations
	time.Sleep(100 * time.Millisecond)

	// Flush the reservoir by resetting the window
	p := proc.(*reservoirSamplerProcessor)
	p.resetWindow(time.Now().UnixNano())

	// Wait for flush to complete
	time.Sleep(100 * time.Millisecond)

	// Verify both traces were processed
	assert.GreaterOrEqual(t, len(sink.AllTraces()), 2, "Expected at least two traces to be processed")
}

func TestTraceAwareVsStandardSampling(t *testing.T) {
	// Create two temporary directories for the checkpoint files
	tempDir, err := os.MkdirTemp("", "reservoir-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	checkpointPath1 := filepath.Join(tempDir, "trace_aware.db")
	checkpointPath2 := filepath.Join(tempDir, "standard.db")

	// Common test parameters
	const (
		reservoirSize = 5
		traceCount    = 20
		spansPerTrace = 4
	)

	// Create a trace-aware processor
	traceAwareCfg := &Config{
		SizeK:              reservoirSize,
		WindowDuration:     "1s",
		CheckpointPath:     checkpointPath1,
		CheckpointInterval: "500ms",
		TraceAware:         true,
		TraceTimeout:       "100ms",
	}

	// Create a standard processor
	standardCfg := &Config{
		SizeK:              reservoirSize,
		WindowDuration:     "1s",
		CheckpointPath:     checkpointPath2,
		CheckpointInterval: "500ms",
		TraceAware:         false,
	}

	// Create sinks and settings
	traceAwareSink := new(consumertest.TracesSink)
	standardSink := new(consumertest.TracesSink)
	set := processor.CreateSettings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
		BuildInfo: component.BuildInfo{},
	}

	// Create processors
	traceAwareProc, err := newProcessor(set, traceAwareCfg, traceAwareSink)
	require.NoError(t, err)
	standardProc, err := newProcessor(set, standardCfg, standardSink)
	require.NoError(t, err)

	// Ensure processors are started
	err = traceAwareProc.Start(context.Background(), nil)
	require.NoError(t, err)
	defer traceAwareProc.Shutdown(context.Background())

	err = standardProc.Start(context.Background(), nil)
	require.NoError(t, err)
	defer standardProc.Shutdown(context.Background())

	// Create multiple traces, each with spansPerTrace spans
	for i := 0; i < traceCount; i++ {
		traces := createTraceWithMultipleSpans(i, spansPerTrace)

		// Process with both processors
		err = traceAwareProc.ConsumeTraces(context.Background(), traces)
		require.NoError(t, err)

		err = standardProc.ConsumeTraces(context.Background(), traces)
		require.NoError(t, err)
	}

	// Wait for trace timeouts to occur
	time.Sleep(150 * time.Millisecond)

	// Flush both reservoirs
	taProc := traceAwareProc.(*reservoirSamplerProcessor)
	taProc.resetWindow(time.Now().UnixNano())

	stdProc := standardProc.(*reservoirSamplerProcessor)
	stdProc.resetWindow(time.Now().UnixNano())

	// Wait for flushes to complete
	time.Sleep(100 * time.Millisecond)

	// Verify standard sampling processed approximately reservoirSize spans
	standardSpanCount := 0
	for _, traces := range standardSink.AllTraces() {
		standardSpanCount += traces.SpanCount()
	}
	assert.InDelta(t, reservoirSize, standardSpanCount, 1,
		"Standard sampling should have about reservoirSize spans")

	// Verify trace-aware sampling processed approximately reservoirSize * spansPerTrace spans
	// (since it samples complete traces, not individual spans)
	traceAwareSpanCount := 0
	for _, traces := range traceAwareSink.AllTraces() {
		traceAwareSpanCount += traces.SpanCount()
	}

	// The trace-aware sampler should sample approximately reservoirSize traces,
	// which means approximately reservoirSize * spansPerTrace spans
	// But we allow some flexibility since the exact number can vary due to
	// randomness in the reservoir sampling algorithm
	expectedMinSpans := reservoirSize
	expectedMaxSpans := reservoirSize * spansPerTrace
	assert.GreaterOrEqual(t, traceAwareSpanCount, expectedMinSpans,
		"Trace-aware sampling should sample at least reservoirSize spans")
	assert.LessOrEqual(t, traceAwareSpanCount, expectedMaxSpans,
		"Trace-aware sampling should sample at most reservoirSize * spansPerTrace spans")

	// Additionally, the trace-aware spans should come in complete traces
	traceAwareTraceIDs := make(map[string]int)
	for _, traces := range traceAwareSink.AllTraces() {
		rss := traces.ResourceSpans()
		for i := 0; i < rss.Len(); i++ {
			sss := rss.At(i).ScopeSpans()
			for j := 0; j < sss.Len(); j++ {
				spans := sss.At(j).Spans()
				for k := 0; k < spans.Len(); k++ {
					traceID := spans.At(k).TraceID().String()
					traceAwareTraceIDs[traceID]++
				}
			}
		}
	}

	// Check that we have complete traces (each trace ID should have spansPerTrace spans)
	for traceID, count := range traceAwareTraceIDs {
		assert.Equal(t, spansPerTrace, count,
			"Trace %s should have %d spans, but has %d", traceID, spansPerTrace, count)
	}
}

// Helper to create a trace with multiple spans sharing the same trace ID
func createTraceWithMultipleSpans(traceIndex, spanCount int) ptrace.Traces {
	traces := ptrace.NewTraces()

	// Generate a unique trace ID for this trace
	traceID := [16]byte{}
	for j := 0; j < 16; j++ {
		traceID[j] = byte((traceIndex*16 + j) % 256)
	}

	// Create a root span
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "test-service")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("test-scope")

	// Root span
	rootSpan := ss.Spans().AppendEmpty()
	rootSpan.SetTraceID(pcommon.TraceID(traceID))
	rootSpanID := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	rootSpan.SetSpanID(pcommon.SpanID(rootSpanID))
	rootSpan.SetName("root-span")
	rootSpan.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	rootSpan.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(time.Millisecond * 100)))

	// Add child spans
	for i := 1; i < spanCount; i++ {
		childSpan := ss.Spans().AppendEmpty()
		childSpan.SetTraceID(pcommon.TraceID(traceID))
		
		// Generate a unique span ID
		spanID := [8]byte{}
		for j := 0; j < 8; j++ {
			spanID[j] = byte((i*8 + j) % 256)
		}
		childSpan.SetSpanID(pcommon.SpanID(spanID))
		
		// Set parent span ID to the root span
		childSpan.SetParentSpanID(pcommon.SpanID(rootSpanID))
		
		childSpan.SetName(fmt.Sprintf("child-span-%d", i))
		childSpan.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(time.Millisecond * 10 * time.Duration(i))))
		childSpan.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(time.Millisecond * (100 + 10*time.Duration(i)))))
		
		// Add some attributes
		childSpan.Attributes().PutInt("child_index", int64(i))
	}

	return traces
}

// Helper to create a trace with parent and child spans
func createTraceWithParentChild() ptrace.Traces {
	traces := ptrace.NewTraces()

	// Generate a unique trace ID for all spans in this trace
	traceID := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

	// Parent span
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "test-service-1")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("test-scope-1")

	parentSpan := ss.Spans().AppendEmpty()
	parentSpan.SetTraceID(pcommon.TraceID(traceID))
	parentSpanID := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	parentSpan.SetSpanID(pcommon.SpanID(parentSpanID))
	parentSpan.SetName("parent-span")
	parentSpan.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	parentSpan.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(time.Millisecond * 100)))
	
	// Note: This is NOT a root span, as we're not setting ParentSpanID to empty
	// We're simulating an incomplete trace by having no root span

	// Child span
	rs2 := traces.ResourceSpans().AppendEmpty()
	rs2.Resource().Attributes().PutStr("service.name", "test-service-2")
	ss2 := rs2.ScopeSpans().AppendEmpty()
	ss2.Scope().SetName("test-scope-2")

	childSpan := ss2.Spans().AppendEmpty()
	childSpan.SetTraceID(pcommon.TraceID(traceID))
	childSpan.SetSpanID(pcommon.SpanID([8]byte{8, 7, 6, 5, 4, 3, 2, 1}))
	childSpan.SetParentSpanID(pcommon.SpanID(parentSpanID))
	childSpan.SetName("child-span")
	childSpan.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(time.Millisecond * 10)))
	childSpan.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(time.Millisecond * 90)))

	return traces
}

// Helper to create a trace with just a root span
func createTraceWithRootSpan() ptrace.Traces {
	traces := ptrace.NewTraces()

	// Generate a unique trace ID
	traceID := [16]byte{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}

	// Root span (no parent span ID)
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "test-service-root")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("test-scope-root")

	rootSpan := ss.Spans().AppendEmpty()
	rootSpan.SetTraceID(pcommon.TraceID(traceID))
	rootSpan.SetSpanID(pcommon.SpanID([8]byte{9, 8, 7, 6, 5, 4, 3, 2}))
	// Not setting ParentSpanID makes this a root span
	rootSpan.SetName("root-only-span")
	rootSpan.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	rootSpan.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(time.Millisecond * 50)))

	return traces
}