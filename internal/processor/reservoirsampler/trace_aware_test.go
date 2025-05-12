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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestTraceAwareBuffer_AddSpan(t *testing.T) {
	// Create a trace ID
	traceID := pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})

	// Create a new trace buffer
	buffer := newTraceBuffer(traceID)

	// Create a span
	span := ptrace.NewSpan()
	span.SetTraceID(traceID)
	span.SetSpanID(pcommon.SpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8}))
	span.SetName("test-span")

	// Create resource and scope
	resource := pcommon.NewResource()
	resource.Attributes().PutStr("service.name", "test-service")

	scope := pcommon.NewInstrumentationScope()
	scope.SetName("test-scope")
	scope.SetVersion("1.0.0")

	// Add the span to the buffer
	buffer.addSpan(span, resource, scope)

	// Verify the span was added
	assert.Equal(t, 1, len(buffer.spans))
	assert.Equal(t, "test-span", buffer.spans[0].span.Name())
	assert.Equal(t, "test-service", buffer.spans[0].resource.Attributes().AsRaw()["service.name"])
	assert.Equal(t, "test-scope", buffer.spans[0].scope.Name())
}

func TestTraceAwareBuffer_RootSpanDetection(t *testing.T) {
	// Create a trace ID
	traceID := pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})

	// Create a new trace buffer
	buffer := newTraceBuffer(traceID)

	// Create a non-root span (with parent)
	childSpan := ptrace.NewSpan()
	childSpan.SetTraceID(traceID)
	childSpan.SetSpanID(pcommon.SpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8}))
	childSpan.SetParentSpanID(pcommon.SpanID([8]byte{8, 7, 6, 5, 4, 3, 2, 1}))
	childSpan.SetName("child-span")

	// Add the child span
	buffer.addSpan(childSpan, pcommon.NewResource(), pcommon.NewInstrumentationScope())

	// Verify the trace is not complete (no root span)
	assert.False(t, buffer.isComplete())

	// Create a root span (no parent)
	rootSpan := ptrace.NewSpan()
	rootSpan.SetTraceID(traceID)
	rootSpan.SetSpanID(pcommon.SpanID([8]byte{8, 7, 6, 5, 4, 3, 2, 1}))
	rootSpan.SetName("root-span")
	// Don't set a parent span ID (makes it a root span)

	// Add the root span
	buffer.addSpan(rootSpan, pcommon.NewResource(), pcommon.NewInstrumentationScope())

	// Verify the trace is now complete (has a root span)
	assert.True(t, buffer.isComplete())
	assert.Equal(t, 2, len(buffer.spans))
}

func TestTraceAwareBuffer_ToTraces(t *testing.T) {
	// Create a trace ID
	traceID := pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})

	// Create a new trace buffer
	buffer := newTraceBuffer(traceID)

	// Create resources and scopes
	resource1 := pcommon.NewResource()
	resource1.Attributes().PutStr("service.name", "service-1")

	resource2 := pcommon.NewResource()
	resource2.Attributes().PutStr("service.name", "service-2")

	scope1 := pcommon.NewInstrumentationScope()
	scope1.SetName("scope-1")

	// Create spans from different resources/scopes
	span1 := ptrace.NewSpan()
	span1.SetTraceID(traceID)
	span1.SetSpanID(pcommon.SpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8}))
	span1.SetName("span-1")

	span2 := ptrace.NewSpan()
	span2.SetTraceID(traceID)
	span2.SetSpanID(pcommon.SpanID([8]byte{8, 7, 6, 5, 4, 3, 2, 1}))
	span2.SetName("span-2")

	span3 := ptrace.NewSpan()
	span3.SetTraceID(traceID)
	span3.SetSpanID(pcommon.SpanID([8]byte{2, 3, 4, 5, 6, 7, 8, 9}))
	span3.SetName("span-3")

	// Add spans to the buffer
	buffer.addSpan(span1, resource1, scope1)
	buffer.addSpan(span2, resource1, scope1) // Same resource/scope as span1
	buffer.addSpan(span3, resource2, scope1) // Different resource, same scope

	// Convert to traces
	traces := buffer.toTraces()

	// Verify the resulting traces structure
	require.Equal(t, 2, traces.ResourceSpans().Len(), "Should have 2 resources")

	// Find resource 1
	var rs1 ptrace.ResourceSpans
	var rs2 ptrace.ResourceSpans

	if traces.ResourceSpans().At(0).Resource().Attributes().AsRaw()["service.name"] == "service-1" {
		rs1 = traces.ResourceSpans().At(0)
		rs2 = traces.ResourceSpans().At(1)
	} else {
		rs1 = traces.ResourceSpans().At(1)
		rs2 = traces.ResourceSpans().At(0)
	}

	assert.Equal(t, "service-1", rs1.Resource().Attributes().AsRaw()["service.name"])
	assert.Equal(t, 1, rs1.ScopeSpans().Len())
	assert.Equal(t, "scope-1", rs1.ScopeSpans().At(0).Scope().Name())
	assert.Equal(t, 2, rs1.ScopeSpans().At(0).Spans().Len())

	assert.Equal(t, "service-2", rs2.Resource().Attributes().AsRaw()["service.name"])
	assert.Equal(t, 1, rs2.ScopeSpans().Len())
	assert.Equal(t, "scope-1", rs2.ScopeSpans().At(0).Scope().Name())
	assert.Equal(t, 1, rs2.ScopeSpans().At(0).Spans().Len())
}

func TestTraceAwareBuffer_Timeout(t *testing.T) {
	// Create a trace ID
	traceID := pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})

	// Create a new trace buffer with an old update time
	buffer := newTraceBuffer(traceID)
	buffer.updateTime = time.Now().Add(-2 * time.Second)

	// Verify that the buffer times out with a 1-second timeout
	assert.True(t, buffer.isTimedOut(1*time.Second))

	// Verify it doesn't time out with a 3-second timeout
	assert.False(t, buffer.isTimedOut(3*time.Second))
}

func TestTraceAwareBufferMap_AddSpan(t *testing.T) {
	// Create a trace buffer map with a long TTL
	tbm := newTraceBufferMap(10*time.Second, 1*time.Second)
	defer tbm.shutdown()

	// Create trace IDs
	traceID1 := pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	traceID2 := pcommon.TraceID([16]byte{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1})

	// Create spans
	childSpan := ptrace.NewSpan()
	childSpan.SetTraceID(traceID1)
	childSpan.SetSpanID(pcommon.SpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8}))
	childSpan.SetParentSpanID(pcommon.SpanID([8]byte{8, 7, 6, 5, 4, 3, 2, 1}))

	rootSpan := ptrace.NewSpan()
	rootSpan.SetTraceID(traceID1)
	rootSpan.SetSpanID(pcommon.SpanID([8]byte{8, 7, 6, 5, 4, 3, 2, 1}))

	orphanSpan := ptrace.NewSpan()
	orphanSpan.SetTraceID(traceID2)
	orphanSpan.SetSpanID(pcommon.SpanID([8]byte{1, 1, 1, 1, 1, 1, 1, 1}))
	orphanSpan.SetParentSpanID(pcommon.SpanID([8]byte{2, 2, 2, 2, 2, 2, 2, 2}))

	// Add child span (shouldn't complete the trace)
	completedBuffer := tbm.addSpan(childSpan, pcommon.NewResource(), pcommon.NewInstrumentationScope())
	assert.Nil(t, completedBuffer)

	// Add root span (should complete the trace)
	completedBuffer = tbm.addSpan(rootSpan, pcommon.NewResource(), pcommon.NewInstrumentationScope())
	assert.NotNil(t, completedBuffer)
	assert.Equal(t, traceID1, completedBuffer.traceID)
	assert.Equal(t, 2, len(completedBuffer.spans))

	// Add orphan span (no root, so trace won't complete immediately)
	completedBuffer = tbm.addSpan(orphanSpan, pcommon.NewResource(), pcommon.NewInstrumentationScope())
	assert.Nil(t, completedBuffer)
}

func TestTraceAwareBufferMap_Cleanup(t *testing.T) {
	// Create a trace buffer map with a very short TTL
	tbm := newTraceBufferMap(50*time.Millisecond, 100*time.Millisecond)
	defer tbm.shutdown()

	// Create trace ID
	traceID := pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})

	// Create a span without a parent (orphan)
	orphanSpan := ptrace.NewSpan()
	orphanSpan.SetTraceID(traceID)
	orphanSpan.SetSpanID(pcommon.SpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8}))
	orphanSpan.SetParentSpanID(pcommon.SpanID([8]byte{8, 7, 6, 5, 4, 3, 2, 1}))

	// Add an orphan span (no root span, so won't be completed)
	tbm.addSpan(orphanSpan, pcommon.NewResource(), pcommon.NewInstrumentationScope())

	// Verify the map has one entry
	tbm.lock.Lock()
	assert.Equal(t, 1, len(tbm.buffers))
	tbm.lock.Unlock()

	// Wait for the cleaner to run (a bit more than the TTL)
	time.Sleep(200 * time.Millisecond)

	// Verify the map is now empty (trace timed out and was removed)
	tbm.lock.Lock()
	assert.Equal(t, 0, len(tbm.buffers))
	tbm.lock.Unlock()
}

func TestTraceAwareBufferMap_GetCompletedAndTimedOut(t *testing.T) {
	// Create a trace buffer map with a short TTL
	tbm := newTraceBufferMap(100*time.Millisecond, 200*time.Millisecond)
	defer tbm.shutdown()

	// Create trace IDs
	traceID1 := pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	traceID2 := pcommon.TraceID([16]byte{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1})

	// Create spans
	rootSpan := ptrace.NewSpan()
	rootSpan.SetTraceID(traceID1)
	rootSpan.SetSpanID(pcommon.SpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8}))

	orphanSpan := ptrace.NewSpan()
	orphanSpan.SetTraceID(traceID2)
	orphanSpan.SetSpanID(pcommon.SpanID([8]byte{1, 1, 1, 1, 1, 1, 1, 1}))
	orphanSpan.SetParentSpanID(pcommon.SpanID([8]byte{2, 2, 2, 2, 2, 2, 2, 2}))

	// Add spans
	tbm.addSpan(rootSpan, pcommon.NewResource(), pcommon.NewInstrumentationScope())
	tbm.addSpan(orphanSpan, pcommon.NewResource(), pcommon.NewInstrumentationScope())

	// Manually add the trace buffer for the orphan back to simulate incomplete trace
	tbm.lock.Lock()
	traceBuffer := newTraceBuffer(traceID2)
	traceBuffer.addSpan(orphanSpan, pcommon.NewResource(), pcommon.NewInstrumentationScope())
	traceBuffer.updateTime = time.Now().Add(-200 * time.Millisecond) // Make it old enough to time out
	tbm.buffers[traceID2.String()] = traceBuffer
	tbm.lock.Unlock()

	// Get completed and timed out traces
	traces := tbm.getCompletedAndTimedOutTraces()

	// Should only get the timed out trace
	assert.Equal(t, 1, len(traces))
	assert.Equal(t, traceID2, traces[0].traceID)

	// Verify the map is now empty
	tbm.lock.Lock()
	assert.Equal(t, 0, len(tbm.buffers))
	tbm.lock.Unlock()
}
