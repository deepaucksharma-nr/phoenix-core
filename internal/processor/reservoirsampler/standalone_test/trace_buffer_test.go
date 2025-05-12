package standalone_test

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// Mock implementation of trace buffer structures for testing

type MockTraceID [16]byte

func (t MockTraceID) String() string {
	return fmt.Sprintf("%x", t[:])
}

type MockSpanID [8]byte

type MockSpan struct {
	TraceID     MockTraceID
	SpanID      MockSpanID
	ParentSpanID MockSpanID
	Name        string
}

func (s MockSpan) IsRoot() bool {
	return s.ParentSpanID == MockSpanID{}
}

type MockResource struct {
	Attributes map[string]string
}

type MockScope struct {
	Name    string
	Version string
}

type SpanWithContext struct {
	Span     MockSpan
	Resource MockResource
	Scope    MockScope
}

// traceBuffer holds spans for a single trace until the trace is complete or times out
type TraceBuffer struct {
	TraceID      MockTraceID
	Spans        []SpanWithContext
	HasRootSpan  bool
	CreationTime time.Time
	UpdateTime   time.Time
}

// NewTraceBuffer creates a new trace buffer for the given trace ID
func NewTraceBuffer(traceID MockTraceID) *TraceBuffer {
	now := time.Now()
	return &TraceBuffer{
		TraceID:      traceID,
		Spans:        make([]SpanWithContext, 0, 4), // Start with capacity for 4 spans
		HasRootSpan:  false,
		CreationTime: now,
		UpdateTime:   now,
	}
}

// IsComplete determines if a trace buffer has enough information to be considered complete
func (tb *TraceBuffer) IsComplete() bool {
	// A trace is considered complete if it has a root span
	return tb.HasRootSpan
}

// IsTimedOut determines if a trace buffer has exceeded its timeout
func (tb *TraceBuffer) IsTimedOut(timeout time.Duration) bool {
	return time.Since(tb.UpdateTime) > timeout
}

// AddSpan adds a span to the trace buffer
func (tb *TraceBuffer) AddSpan(span MockSpan, resource MockResource, scope MockScope) {
	// Check if this is a root span
	if span.IsRoot() {
		tb.HasRootSpan = true
	}

	// Add the span to the buffer
	tb.Spans = append(tb.Spans, SpanWithContext{
		Span:     span,
		Resource: resource,
		Scope:    scope,
	})

	// Update the last updated time
	tb.UpdateTime = time.Now()
}

// traceBufferMap manages a collection of trace buffers
type TraceBufferMap struct {
	Buffers      map[string]*TraceBuffer // Map from trace ID (as string) to buffer
	Lock         sync.Mutex              // Protects access to the map
	BufferTTL    time.Duration           // How long to keep incomplete traces
	CleanupTimer *time.Ticker            // Timer for periodic cleanup
	StopCh       chan struct{}           // Channel to signal shutdown
}

// NewTraceBufferMap creates a new trace buffer map
func NewTraceBufferMap(bufferTTL time.Duration, cleanupInterval time.Duration) *TraceBufferMap {
	tbm := &TraceBufferMap{
		Buffers:      make(map[string]*TraceBuffer),
		BufferTTL:    bufferTTL,
		CleanupTimer: time.NewTicker(cleanupInterval),
		StopCh:       make(chan struct{}),
	}

	// Start periodic cleanup
	go tbm.PeriodicCleanup()

	return tbm
}

// Shutdown stops the cleanup goroutine
func (tbm *TraceBufferMap) Shutdown() {
	close(tbm.StopCh)
	tbm.CleanupTimer.Stop()
}

// PeriodicCleanup removes expired trace buffers
func (tbm *TraceBufferMap) PeriodicCleanup() {
	for {
		select {
		case <-tbm.CleanupTimer.C:
			tbm.Cleanup()
		case <-tbm.StopCh:
			return
		}
	}
}

// Cleanup removes expired trace buffers
func (tbm *TraceBufferMap) Cleanup() {
	tbm.Lock.Lock()
	defer tbm.Lock.Unlock()

	for traceIDStr, buffer := range tbm.Buffers {
		if buffer.IsTimedOut(tbm.BufferTTL) {
			delete(tbm.Buffers, traceIDStr)
		}
	}
}

// AddSpan adds a span to the appropriate trace buffer
func (tbm *TraceBufferMap) AddSpan(span MockSpan, resource MockResource, scope MockScope) *TraceBuffer {
	traceIDStr := span.TraceID.String()

	tbm.Lock.Lock()
	defer tbm.Lock.Unlock()

	// Get or create the trace buffer
	buffer, exists := tbm.Buffers[traceIDStr]
	if !exists {
		buffer = NewTraceBuffer(span.TraceID)
		tbm.Buffers[traceIDStr] = buffer
	}

	// Add the span to the buffer
	buffer.AddSpan(span, resource, scope)

	// If the trace is complete, return it for processing
	if buffer.IsComplete() {
		completedBuffer := buffer
		delete(tbm.Buffers, traceIDStr)
		return completedBuffer
	}

	return nil
}

// GetCompletedAndTimedOutTraces returns all completed and timed out traces, removing them from the map
func (tbm *TraceBufferMap) GetCompletedAndTimedOutTraces() []*TraceBuffer {
	tbm.Lock.Lock()
	defer tbm.Lock.Unlock()

	var result []*TraceBuffer

	for traceIDStr, buffer := range tbm.Buffers {
		if buffer.IsComplete() || buffer.IsTimedOut(tbm.BufferTTL) {
			result = append(result, buffer)
			delete(tbm.Buffers, traceIDStr)
		}
	}

	return result
}

// Unit tests for trace buffer functionality
func TestTraceBuffer_AddSpan(t *testing.T) {
	// Create a trace ID
	traceID := MockTraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

	// Create a new trace buffer
	buffer := NewTraceBuffer(traceID)

	// Create a span
	span := MockSpan{
		TraceID:     traceID,
		SpanID:      MockSpanID{1, 2, 3, 4, 5, 6, 7, 8},
		ParentSpanID: MockSpanID{8, 7, 6, 5, 4, 3, 2, 1},
		Name:        "test-span",
	}

	// Create resource and scope
	resource := MockResource{
		Attributes: map[string]string{"service.name": "test-service"},
	}

	scope := MockScope{
		Name:    "test-scope",
		Version: "1.0.0",
	}

	// Add the span to the buffer
	buffer.AddSpan(span, resource, scope)

	// Verify the span was added
	if len(buffer.Spans) != 1 {
		t.Errorf("Expected 1 span in buffer, got %d", len(buffer.Spans))
	}
	if buffer.Spans[0].Span.Name != "test-span" {
		t.Errorf("Expected span name 'test-span', got '%s'", buffer.Spans[0].Span.Name)
	}
	if buffer.Spans[0].Resource.Attributes["service.name"] != "test-service" {
		t.Errorf("Expected service.name 'test-service', got '%s'", buffer.Spans[0].Resource.Attributes["service.name"])
	}
	if buffer.Spans[0].Scope.Name != "test-scope" {
		t.Errorf("Expected scope name 'test-scope', got '%s'", buffer.Spans[0].Scope.Name)
	}
}

func TestTraceBuffer_RootSpanDetection(t *testing.T) {
	// Create a trace ID
	traceID := MockTraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

	// Create a new trace buffer
	buffer := NewTraceBuffer(traceID)

	// Create a non-root span (with parent)
	childSpan := MockSpan{
		TraceID:     traceID,
		SpanID:      MockSpanID{1, 2, 3, 4, 5, 6, 7, 8},
		ParentSpanID: MockSpanID{8, 7, 6, 5, 4, 3, 2, 1},
		Name:        "child-span",
	}

	// Add the child span
	buffer.AddSpan(childSpan, MockResource{}, MockScope{})

	// Verify the trace is not complete (no root span)
	if buffer.IsComplete() {
		t.Error("Expected trace to be incomplete, but it was complete")
	}

	// Create a root span (no parent)
	rootSpan := MockSpan{
		TraceID:     traceID,
		SpanID:      MockSpanID{8, 7, 6, 5, 4, 3, 2, 1},
		ParentSpanID: MockSpanID{}, // Empty parent ID = root span
		Name:        "root-span",
	}

	// Add the root span
	buffer.AddSpan(rootSpan, MockResource{}, MockScope{})

	// Verify the trace is now complete (has a root span)
	if !buffer.IsComplete() {
		t.Error("Expected trace to be complete, but it was incomplete")
	}
	if len(buffer.Spans) != 2 {
		t.Errorf("Expected 2 spans in buffer, got %d", len(buffer.Spans))
	}
}

func TestTraceBuffer_Timeout(t *testing.T) {
	// Create a trace ID
	traceID := MockTraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

	// Create a new trace buffer with an old update time
	buffer := NewTraceBuffer(traceID)
	buffer.UpdateTime = time.Now().Add(-2 * time.Second)

	// Verify that the buffer times out with a 1-second timeout
	if !buffer.IsTimedOut(1 * time.Second) {
		t.Error("Expected buffer to be timed out with 1s timeout, but it wasn't")
	}

	// Verify it doesn't time out with a 3-second timeout
	if buffer.IsTimedOut(3 * time.Second) {
		t.Error("Expected buffer to not be timed out with 3s timeout, but it was")
	}
}

func TestTraceBufferMap_AddSpan(t *testing.T) {
	// Create a trace buffer map with a long TTL
	tbm := NewTraceBufferMap(10*time.Second, 1*time.Second)
	defer tbm.Shutdown()

	// Create trace IDs
	traceID1 := MockTraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	traceID2 := MockTraceID{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}

	// Create spans
	childSpan := MockSpan{
		TraceID:     traceID1,
		SpanID:      MockSpanID{1, 2, 3, 4, 5, 6, 7, 8},
		ParentSpanID: MockSpanID{8, 7, 6, 5, 4, 3, 2, 1},
		Name:        "child-span",
	}

	rootSpan := MockSpan{
		TraceID:     traceID1,
		SpanID:      MockSpanID{8, 7, 6, 5, 4, 3, 2, 1},
		ParentSpanID: MockSpanID{}, // Empty parent ID = root span
		Name:        "root-span",
	}

	orphanSpan := MockSpan{
		TraceID:     traceID2,
		SpanID:      MockSpanID{1, 1, 1, 1, 1, 1, 1, 1},
		ParentSpanID: MockSpanID{2, 2, 2, 2, 2, 2, 2, 2},
		Name:        "orphan-span",
	}

	// Add child span (shouldn't complete the trace)
	completedBuffer := tbm.AddSpan(childSpan, MockResource{}, MockScope{})
	if completedBuffer != nil {
		t.Error("Expected nil for incomplete trace, got non-nil")
	}

	// Add root span (should complete the trace)
	completedBuffer = tbm.AddSpan(rootSpan, MockResource{}, MockScope{})
	if completedBuffer == nil {
		t.Error("Expected non-nil for complete trace, got nil")
	} else {
		if completedBuffer.TraceID != traceID1 {
			t.Error("Wrong trace ID returned for completed buffer")
		}
		if len(completedBuffer.Spans) != 2 {
			t.Errorf("Expected 2 spans in completed buffer, got %d", len(completedBuffer.Spans))
		}
	}

	// Add orphan span (no root, so trace won't complete immediately)
	completedBuffer = tbm.AddSpan(orphanSpan, MockResource{}, MockScope{})
	if completedBuffer != nil {
		t.Error("Expected nil for incomplete trace, got non-nil")
	}

	// Verify the second trace is in the map
	tbm.Lock.Lock()
	bufferCount := len(tbm.Buffers)
	tbm.Lock.Unlock()
	if bufferCount != 1 {
		t.Errorf("Expected 1 buffer in map, got %d", bufferCount)
	}
}

func TestTraceBufferMap_Cleanup(t *testing.T) {
	// Create a trace buffer map with a very short TTL
	tbm := NewTraceBufferMap(50*time.Millisecond, 100*time.Millisecond)
	defer tbm.Shutdown()

	// Create trace ID
	traceID := MockTraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

	// Create a span without a parent (orphan)
	orphanSpan := MockSpan{
		TraceID:     traceID,
		SpanID:      MockSpanID{1, 2, 3, 4, 5, 6, 7, 8},
		ParentSpanID: MockSpanID{8, 7, 6, 5, 4, 3, 2, 1},
		Name:        "orphan-span",
	}

	// Add an orphan span (no root span, so won't be completed)
	tbm.AddSpan(orphanSpan, MockResource{}, MockScope{})

	// Verify the map has one entry
	tbm.Lock.Lock()
	if len(tbm.Buffers) != 1 {
		t.Errorf("Expected 1 buffer in map before timeout, got %d", len(tbm.Buffers))
	}
	tbm.Lock.Unlock()

	// Wait for the cleaner to run (a bit more than the TTL)
	time.Sleep(200 * time.Millisecond)

	// Verify the map is now empty (trace timed out and was removed)
	tbm.Lock.Lock()
	if len(tbm.Buffers) != 0 {
		t.Errorf("Expected 0 buffers in map after timeout, got %d", len(tbm.Buffers))
	}
	tbm.Lock.Unlock()
}

func TestTraceBufferMap_GetCompletedAndTimedOutTraces(t *testing.T) {
	// Create a trace buffer map with a short TTL
	tbm := NewTraceBufferMap(100*time.Millisecond, 200*time.Millisecond)
	defer tbm.Shutdown()

	// Create trace IDs
	traceID1 := MockTraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	traceID2 := MockTraceID{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}

	// Create spans
	rootSpan := MockSpan{
		TraceID:     traceID1,
		SpanID:      MockSpanID{1, 2, 3, 4, 5, 6, 7, 8},
		ParentSpanID: MockSpanID{}, // Empty parent ID = root span
		Name:        "root-span",
	}

	orphanSpan := MockSpan{
		TraceID:     traceID2,
		SpanID:      MockSpanID{1, 1, 1, 1, 1, 1, 1, 1},
		ParentSpanID: MockSpanID{2, 2, 2, 2, 2, 2, 2, 2},
		Name:        "orphan-span",
	}

	// Add spans - the root span should be immediately completed and removed
	tbm.AddSpan(rootSpan, MockResource{}, MockScope{})
	tbm.AddSpan(orphanSpan, MockResource{}, MockScope{})

	// Manually add the trace buffer for the orphan to simulate incomplete trace
	tbm.Lock.Lock()
	traceBuffer := NewTraceBuffer(traceID2)
	traceBuffer.AddSpan(orphanSpan, MockResource{}, MockScope{})
	traceBuffer.UpdateTime = time.Now().Add(-200 * time.Millisecond) // Make it old enough to time out
	tbm.Buffers[traceID2.String()] = traceBuffer
	tbm.Lock.Unlock()

	// Get completed and timed out traces
	traces := tbm.GetCompletedAndTimedOutTraces()

	// Should only get the timed out trace
	if len(traces) != 1 {
		t.Errorf("Expected 1 timed out trace, got %d", len(traces))
	} else if traces[0].TraceID != traceID2 {
		t.Error("Wrong trace ID returned for timed out trace")
	}

	// Verify the map is now empty
	tbm.Lock.Lock()
	if len(tbm.Buffers) != 0 {
		t.Errorf("Expected empty buffer map after getting timed out traces, got %d buffers", len(tbm.Buffers))
	}
	tbm.Lock.Unlock()
}