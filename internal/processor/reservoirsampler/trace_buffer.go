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

// Package reservoirsampler provides trace-aware reservoir sampling for OpenTelemetry traces.
//
// Trace-Aware Reservoir Sampling:
//
// The trace_buffer.go file implements trace-aware reservoir sampling as specified in
// PTE-DS-V2.2.6/V2.2.7. Traditional reservoir sampling processes individual spans,
// but this can lead to incomplete traces in the reservoir (e.g., child spans without
// their parent spans). Trace-aware sampling addresses this by:
//
// 1. Buffering: Spans are grouped by TraceID in a "trace buffer" that contains all
//    spans belonging to the same trace.
//
// 2. Root Span Detection: A trace is considered "complete" when it contains at least
//    one root span (a span with no parent span ID). This is a heuristic that assumes
//    a trace with its root is reasonably complete.
//
// 3. Timeout Mechanism: Since some traces may never receive a root span (due to
//    collector partitioning, lost spans, etc.), a timeout mechanism ensures that
//    incomplete traces don't accumulate indefinitely. After a configurable timeout,
//    incomplete traces are sampled anyway.
//
// 4. Reservoir Sampling Algorithm R: Once a trace is complete (or times out), the
//    entire trace is treated as a single entry in the reservoir. This ensures that
//    when a trace is sampled, all of its spans are kept together.
//
// The implementation is designed to handle concurrent span arrivals efficiently and
// provides proper cleanup mechanisms to prevent memory leaks from abandoned traces.

package reservoirsampler

import (
	"sync"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// traceBuffer holds spans for a single trace until the trace is complete or times out
type traceBuffer struct {
	traceID      pcommon.TraceID
	spans        []spanWithContext
	hasRootSpan  bool
	creationTime time.Time
	updateTime   time.Time
}

// spanWithContext holds a span and its context (resource and scope)
type spanWithContext struct {
	span     ptrace.Span
	resource pcommon.Resource
	scope    pcommon.InstrumentationScope
}

// newTraceBuffer creates a new trace buffer for the given trace ID
func newTraceBuffer(traceID pcommon.TraceID) *traceBuffer {
	now := time.Now()
	return &traceBuffer{
		traceID:      traceID,
		spans:        make([]spanWithContext, 0, 4), // Start with capacity for 4 spans
		hasRootSpan:  false,
		creationTime: now,
		updateTime:   now,
	}
}

// isRoot determines if a span is a root span (no parent span ID)
func isRoot(span ptrace.Span) bool {
	// In OTLP, a span with an empty parent span ID is a root span
	return span.ParentSpanID().IsEmpty()
}

// isComplete determines if a trace buffer has enough information to be considered complete
func (tb *traceBuffer) isComplete() bool {
	// A trace is considered complete if it has a root span
	return tb.hasRootSpan
}

// isTimedOut determines if a trace buffer has exceeded its timeout
func (tb *traceBuffer) isTimedOut(timeout time.Duration) bool {
	return time.Since(tb.updateTime) > timeout
}

// addSpan adds a span to the trace buffer
func (tb *traceBuffer) addSpan(span ptrace.Span, resource pcommon.Resource, scope pcommon.InstrumentationScope) {
	// Create copies of the span, resource, and scope
	spanCopy := ptrace.NewSpan()
	span.CopyTo(spanCopy)

	resourceCopy := pcommon.NewResource()
	resource.CopyTo(resourceCopy)

	scopeCopy := pcommon.NewInstrumentationScope()
	scope.CopyTo(scopeCopy)

	// Check if this is a root span
	if isRoot(spanCopy) {
		tb.hasRootSpan = true
	}

	// Add the span to the buffer
	tb.spans = append(tb.spans, spanWithContext{
		span:     spanCopy,
		resource: resourceCopy,
		scope:    scopeCopy,
	})

	// Update the last updated time
	tb.updateTime = time.Now()
}

// toTraces converts the trace buffer to ptrace.Traces for processing
func (tb *traceBuffer) toTraces() ptrace.Traces {
	traces := ptrace.NewTraces()

	// Map to track resource/scope combinations we've already added
	resourceScopeMap := make(map[string]struct {
		resourceIdx int
		scopeIdx    int
	})

	// For each span in the buffer
	for _, swc := range tb.spans {
		// Generate a key for this resource/scope combination
		resourceKey := swc.resource.Attributes().AsRaw()
		scopeName := swc.scope.Name()
		scopeVersion := swc.scope.Version()
		key := scopeName + scopeVersion + resourceKey.String()

		var resourceIdx, scopeIdx int
		idx, exists := resourceScopeMap[key]

		if !exists {
			// If this is a new resource/scope combination, add it
			resourceIdx = traces.ResourceSpans().Len()
			rs := traces.ResourceSpans().AppendEmpty()
			swc.resource.CopyTo(rs.Resource())

			scopeIdx = rs.ScopeSpans().Len()
			ss := rs.ScopeSpans().AppendEmpty()
			swc.scope.CopyTo(ss.Scope())

			resourceScopeMap[key] = struct {
				resourceIdx int
				scopeIdx    int
			}{resourceIdx, scopeIdx}
		} else {
			// Otherwise use the existing resource/scope
			resourceIdx = idx.resourceIdx
			scopeIdx = idx.scopeIdx
		}

		// Add the span to the appropriate scope
		spanDest := traces.ResourceSpans().At(resourceIdx).ScopeSpans().At(scopeIdx).Spans().AppendEmpty()
		swc.span.CopyTo(spanDest)
	}

	return traces
}

// traceBufferMap manages a collection of trace buffers
type traceBufferMap struct {
	buffers      map[string]*traceBuffer // Map from trace ID (as string) to buffer
	lock         sync.Mutex              // Protects access to the map
	bufferTTL    time.Duration           // How long to keep incomplete traces
	cleanupTimer *time.Ticker            // Timer for periodic cleanup
	stopCh       chan struct{}           // Channel to signal shutdown
}

// newTraceBufferMap creates a new trace buffer map
func newTraceBufferMap(bufferTTL time.Duration, cleanupInterval time.Duration) *traceBufferMap {
	tbm := &traceBufferMap{
		buffers:      make(map[string]*traceBuffer),
		bufferTTL:    bufferTTL,
		cleanupTimer: time.NewTicker(cleanupInterval),
		stopCh:       make(chan struct{}),
	}

	// Start periodic cleanup
	go tbm.periodicCleanup()

	return tbm
}

// shutdown stops the cleanup goroutine
func (tbm *traceBufferMap) shutdown() {
	close(tbm.stopCh)
	tbm.cleanupTimer.Stop()
}

// periodicCleanup removes expired trace buffers
func (tbm *traceBufferMap) periodicCleanup() {
	for {
		select {
		case <-tbm.cleanupTimer.C:
			tbm.cleanup()
		case <-tbm.stopCh:
			return
		}
	}
}

// cleanup removes expired trace buffers
func (tbm *traceBufferMap) cleanup() {
	tbm.lock.Lock()
	defer tbm.lock.Unlock()

	for traceIDStr, buffer := range tbm.buffers {
		if buffer.isTimedOut(tbm.bufferTTL) {
			delete(tbm.buffers, traceIDStr)
		}
	}
}

// addSpan adds a span to the appropriate trace buffer
func (tbm *traceBufferMap) addSpan(span ptrace.Span, resource pcommon.Resource, scope pcommon.InstrumentationScope) *traceBuffer {
	traceID := span.TraceID()
	traceIDStr := traceID.String()

	tbm.lock.Lock()
	defer tbm.lock.Unlock()

	// Get or create the trace buffer
	buffer, exists := tbm.buffers[traceIDStr]
	if !exists {
		buffer = newTraceBuffer(traceID)
		tbm.buffers[traceIDStr] = buffer
	}

	// Add the span to the buffer
	buffer.addSpan(span, resource, scope)

	// If the trace is complete, return it for processing
	if buffer.isComplete() {
		completedBuffer := buffer
		delete(tbm.buffers, traceIDStr)
		return completedBuffer
	}

	return nil
}

// getCompletedAndTimedOutTraces returns all completed and timed out traces, removing them from the map
func (tbm *traceBufferMap) getCompletedAndTimedOutTraces() []*traceBuffer {
	tbm.lock.Lock()
	defer tbm.lock.Unlock()

	var result []*traceBuffer

	for traceIDStr, buffer := range tbm.buffers {
		if buffer.isComplete() || buffer.isTimedOut(tbm.bufferTTL) {
			result = append(result, buffer)
			delete(tbm.buffers, traceIDStr)
		}
	}

	return result
}
