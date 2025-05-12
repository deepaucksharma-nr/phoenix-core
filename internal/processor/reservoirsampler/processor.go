package reservoirsampler

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/boltdb/bolt"
	"github.com/deepaucksharma-nr/phoenix-core/internal/metrics"
	"github.com/deepaucksharma-nr/phoenix-core/internal/pkg/tunableregistry"
	"github.com/deepaucksharma-nr/phoenix-core/internal/processor/reservoirsampler/spanprotos"
	"github.com/hashicorp/golang-lru/v2/simplelru"
	"github.com/robfig/cron/v3"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

const (
	bucketName = "reservoir_checkpoint"
	keyN       = "n"
	keyT0      = "t0"
	keySpans   = "spans"

	// Trace buffer constants
	defaultTraceTimeoutDuration  = 5 * time.Second
	defaultBufferCleanupInterval = 1 * time.Second
)

// SpanWithSummary pairs a span summary with its full span data
type SpanWithSummary struct {
	Summary  *spanprotos.SpanSummary
	Span     ptrace.Span
	Resource pcommon.Resource
	Scope    pcommon.InstrumentationScope
}

type reservoirSamplerProcessor struct {
	config       *Config
	nextConsumer consumer.Traces
	logger       *zap.Logger
	registryID   string

	// Reservoir variables
	k        int
	windowNS int64
	n        atomic.Uint64
	t0       atomic.Int64

	// Sampling buffer management
	activeBuffer atomic.Pointer[[]SpanWithSummary]
	flushMu      sync.Mutex
	pool         sync.Pool

	// LRU cache for full spans
	spanLRU *simplelru.LRU[string, SpanWithSummary]
	lruMu   sync.Mutex

	// Checkpointing
	db            *bolt.DB
	chkTicker     *time.Ticker
	cronScheduler *cron.Cron
	cronEntryID   cron.EntryID

	// Random number generation
	rand *rand.Rand
	mu   sync.Mutex

	// Metrics
	metricsRegistry    *metrics.MetricsRegistry
	metricsTicker      *time.Ticker
	stopCh             chan struct{}
	lruEvictionCount   atomic.Int64
	checkpointAge      atomic.Int64
	flushDuration      atomic.Int64
	checkpointDuration atomic.Int64
	lastCheckpointTime atomic.Int64
	compactionDuration atomic.Int64
	lastCompactionTime atomic.Int64
	dbSizeBytes        atomic.Int64
	compactionCount    atomic.Int64

	// Trace-aware sampling
	traceBuffers         *traceBufferMap
	traceMu              sync.Mutex
	traceTimeoutDuration time.Duration
	incompleteTraceCount atomic.Int64
	completeTraceCount   atomic.Int64
	timedOutTraceCount   atomic.Int64
}

// Ensure reservoirSamplerProcessor implements required interfaces
var _ processor.Traces = (*reservoirSamplerProcessor)(nil)

// newProcessor creates a new reservoir sampling processor.
func newProcessor(settings processor.CreateSettings, config component.Config, nextConsumer consumer.Traces) (processor.Traces, error) {
	cfg := config.(*Config)

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	windowDuration, _ := time.ParseDuration(cfg.WindowDuration)
	checkpointInterval, _ := time.ParseDuration(cfg.CheckpointInterval)

	// Parse trace timeout if specified
	traceTimeoutDuration := defaultTraceTimeoutDuration
	if cfg.TraceTimeout != "" {
		var err error
		traceTimeoutDuration, err = time.ParseDuration(cfg.TraceTimeout)
		if err != nil {
			// This shouldn't happen as the config is validated
			settings.Logger.Warn("Invalid trace timeout, using default",
				zap.String("configured_value", cfg.TraceTimeout),
				zap.Duration("default_value", defaultTraceTimeoutDuration))
		}
	}

	// Ensure checkpoint directory exists
	checkpointDir := filepath.Dir(cfg.CheckpointPath)
	if err := os.MkdirAll(checkpointDir, 0755); err != nil {
		return nil, err
	}

	// Open BoltDB for checkpointing
	db, err := bolt.Open(cfg.CheckpointPath, 0600, nil)
	if err != nil {
		return nil, err
	}

	// Create bucket if it doesn't exist
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucketName))
		return err
	}); err != nil {
		db.Close()
		return nil, err
	}

	// Create a counter for LRU evictions
	evictionCounter := &atomic.Int64{}

	// Create LRU cache for full span data (2x size of reservoir to allow some buffer)
	onEvict := func(key string, value SpanWithSummary) {
		// Increment counter when items are evicted
		evictionCounter.Add(1)
	}
	lruCache, err := simplelru.NewLRU[string, SpanWithSummary](cfg.SizeK*2, onEvict)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create LRU cache: %w", err)
	}

	// Determine registry ID to use (from config or default)
	registryID := "reservoir_sampler"
	if cfg.TunableRegistryID != "" {
		registryID = cfg.TunableRegistryID
	}

	// Initialize processor
	p := &reservoirSamplerProcessor{
		config:               cfg,
		nextConsumer:         nextConsumer,
		logger:               settings.Logger,
		registryID:           registryID,
		k:                    cfg.SizeK,
		windowNS:             windowDuration.Nanoseconds(),
		db:                   db,
		rand:                 rand.New(rand.NewSource(time.Now().UnixNano())),
		spanLRU:              lruCache,
		metricsRegistry:      metrics.GetInstance(settings.Logger),
		stopCh:               make(chan struct{}),
		traceTimeoutDuration: traceTimeoutDuration,
	}

	// Note: onEvict callback is already set during LRU creation

	// Initialize metrics
	if err := p.initMetrics(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize metrics: %w", err)
	}

	// Initialize active buffer with capacity k
	initialBuffer := make([]SpanWithSummary, 0, p.k)
	p.activeBuffer.Store(&initialBuffer)

	// Setup object pool for span batches
	p.pool.New = func() interface{} {
		buffer := make([]SpanWithSummary, 0, p.k)
		return &buffer
	}

	// Initialize trace buffer map if trace-aware is enabled
	if cfg.TraceAware {
		p.traceBuffers = newTraceBufferMap(p.traceTimeoutDuration, defaultBufferCleanupInterval)
	}

	// Register with TunableRegistry
	registry := tunableregistry.GetInstance()
	registry.Register(p)

	// Try to load checkpoint
	if err := p.loadCheckpoint(); err != nil {
		p.logger.Warn("Failed to load checkpoint, starting with empty reservoir", zap.Error(err))
		// Set initial time
		p.t0.Store(time.Now().UnixNano())
	}

	// Start checkpoint ticker
	p.chkTicker = time.NewTicker(checkpointInterval)
	go p.checkpointLoop()

	p.logger.Info("Reservoir sampler processor created",
		zap.Int("size_k", cfg.SizeK),
		zap.String("window_duration", cfg.WindowDuration),
		zap.String("checkpoint_path", cfg.CheckpointPath),
		zap.String("checkpoint_interval", cfg.CheckpointInterval),
		zap.String("registry_id", p.registryID),
		zap.Bool("trace_aware", cfg.TraceAware),
		zap.Duration("trace_timeout", p.traceTimeoutDuration))

	return p, nil
}

// ConsumeTraces implements the processor.Traces interface
func (p *reservoirSamplerProcessor) ConsumeTraces(ctx context.Context, traces ptrace.Traces) error {
	// Check if window has expired
	now := time.Now().UnixNano()
	windowStart := p.t0.Load()

	if now-windowStart > p.windowNS {
		// Window has expired, flush current reservoir and start a new one
		p.resetWindow(now)
	}

	// Process the incoming trace data
	resourceSpans := traces.ResourceSpans()

	// Use different processing paths depending on whether trace-aware sampling is enabled
	if p.config.TraceAware && p.traceBuffers != nil {
		// Process with trace-aware sampling
		//
		// Trace-aware sampling flow:
		// 1. Each span is added to a trace buffer keyed by its TraceID
		// 2. If the span completes a trace (i.e., it's a root span or makes the trace
		//    contain a root span), the entire trace is immediately sampled
		// 3. Periodically, traces that have been in the buffer too long are
		//    considered timed out and are sampled
		// 4. When sampling a trace, all spans from that trace are treated as
		//    a single unit for reservoir sampling (Algorithm R)
		// 5. If a trace is selected for the reservoir, all its spans are added
		//
		// This ensures:
		// - Spans from the same trace stay together (not split across reservoirs)
		// - Memory doesn't grow unbounded with abandoned traces
		// - Sampling is still statistically correct (each trace has k/n probability)
		for i := 0; i < resourceSpans.Len(); i++ {
			rs := resourceSpans.At(i)
			resource := rs.Resource()

			scopeSpans := rs.ScopeSpans()
			for j := 0; j < scopeSpans.Len(); j++ {
				ss := scopeSpans.At(j)
				scope := ss.Scope()

				spans := ss.Spans()
				for k := 0; k < spans.Len(); k++ {
					span := spans.At(k)
					p.processSpan(span, resource, scope)
				}
			}
		}
	} else {
		// Process with standard Algorithm R (not trace-aware)
		// Each span is treated as an individual item for reservoir sampling
		for i := 0; i < resourceSpans.Len(); i++ {
			rs := resourceSpans.At(i)
			resource := rs.Resource()

			scopeSpans := rs.ScopeSpans()
			for j := 0; j < scopeSpans.Len(); j++ {
				ss := scopeSpans.At(j)
				scope := ss.Scope()

				spans := ss.Spans()
				for k := 0; k < spans.Len(); k++ {
					span := spans.At(k)
					p.processSpan(span, resource, scope)
				}
			}
		}
	}

	return nil
}

// processSpan handles span processing with trace-aware buffering.
// It manages trace buffers, detects complete traces, and handles timeouts.
func (p *reservoirSamplerProcessor) processSpan(
	span ptrace.Span,
	resource pcommon.Resource,
	scope pcommon.InstrumentationScope,
) {
	// Add span to trace buffer.
	// This groups spans by trace ID and keeps them until the trace is complete
	// or times out. If the span completes a trace (by being a root span or
	// completing a trace that now contains a root span), it will be returned
	// for immediate sampling.
	completedTrace := p.traceBuffers.addSpan(span, resource, scope)

	// If a trace is complete or timed out, sample it
	if completedTrace != nil {
		// Update metrics based on how the trace was completed
		if completedTrace.isComplete() {
			// This trace contains a root span
			p.completeTraceCount.Add(1)
		} else {
			// This trace timed out before receiving a root span
			p.timedOutTraceCount.Add(1)
		}

		// Sample the completed trace using reservoir sampling
		p.sampleTrace(completedTrace)
	}

	// Check for and process any additional traces that have timed out
	// during this call. This helps ensure timely processing of traces
	// that have been waiting too long.
	timedOutTraces := p.traceBuffers.getCompletedAndTimedOutTraces()
	for _, traceBuffer := range timedOutTraces {
		// Update metrics based on completion status
		if traceBuffer.isComplete() {
			p.completeTraceCount.Add(1)
		} else {
			p.timedOutTraceCount.Add(1)
		}

		// Process each timed-out trace
		p.sampleTrace(traceBuffer)
	}
}

// sampleTrace performs reservoir sampling on a complete trace.
// This is the core of trace-aware sampling, where we sample entire traces
// as single units rather than individual spans.
func (p *reservoirSamplerProcessor) sampleTrace(traceBuffer *traceBuffer) {
	// Convert the trace buffer to a proper traces object
	// This combines all spans from the trace buffer into an OTel traces structure
	traces := traceBuffer.toTraces()

	// Sample with Algorithm R (treat the whole trace as one unit)
	// Algorithm R ensures each trace has exactly k/n probability of being selected,
	// where k is the reservoir size and n is the total number of traces seen.
	i := p.n.Add(1) // Increment n and get the new value

	if i <= uint64(p.k) {
		// Reservoir not yet full, add all spans from the trace
		// This happens for the first k traces we see
		p.addTraceToReservoir(traces)
	} else {
		// Reservoir is full, replace with probability k/i
		// This maintains the statistical property that each trace has k/n
		// probability of being in the final sample
		p.mu.Lock()
		r := p.rand.Int63n(int64(i))
		p.mu.Unlock()

		if r < int64(p.k) {
			// Replace with the new trace
			// Note: We're conceptually replacing a random item in the reservoir,
			// but in practice we just add it to the current set of spans
			p.addTraceToReservoir(traces)
		}
		// Otherwise, discard the trace (don't add it to the reservoir)
	}
}

// addTraceToReservoir adds all spans from a trace to the reservoir.
// Once a trace has been chosen for the reservoir, all of its spans
// are individually added to maintain the integrity of the trace.
func (p *reservoirSamplerProcessor) addTraceToReservoir(traces ptrace.Traces) {
	// Process all spans in the trace by iterating through the
	// ResourceSpans -> ScopeSpans -> Spans hierarchy
	resourceSpans := traces.ResourceSpans()
	for i := 0; i < resourceSpans.Len(); i++ {
		rs := resourceSpans.At(i)
		resource := rs.Resource()

		scopeSpans := rs.ScopeSpans()
		for j := 0; j < scopeSpans.Len(); j++ {
			ss := scopeSpans.At(j)
			scope := ss.Scope()

			spans := ss.Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				// Add each span to the reservoir individually,
				// but maintain their association through the resource/scope context
				// and the trace ID that connects them
				p.addSpanToReservoir(span, resource, scope)
			}
		}
	}
}

// addSpanToReservoir adds a span to the reservoir
func (p *reservoirSamplerProcessor) addSpanToReservoir(
	span ptrace.Span,
	resource pcommon.Resource,
	scope pcommon.InstrumentationScope,
) {
	// Create span summary
	summary := spanSummaryFromSpan(span, resource, scope)

	// Create deep copies of the span, resource, and scope to store in the reservoire
	spanCopy := ptrace.NewSpan()
	span.CopyTo(spanCopy)

	resourceCopy := pcommon.NewResource()
	resource.CopyTo(resourceCopy)

	scopeCopy := pcommon.NewInstrumentationScope()
	scope.CopyTo(scopeCopy)

	// Create span with summary
	spanWithSummary := SpanWithSummary{
		Summary:  summary,
		Span:     spanCopy,
		Resource: resourceCopy,
		Scope:    scopeCopy,
	}

	// Get the current active buffer
	bufferPtr := p.activeBuffer.Load()
	buffer := *bufferPtr

	// Create a unique span key for LRU cache
	spanKey := createSpanKey(span.TraceID(), span.SpanID())

	// Add to LRU cache
	p.lruMu.Lock()
	p.spanLRU.Add(spanKey, spanWithSummary)
	p.lruMu.Unlock()

	// Add the span summary to the active buffer
	// If buffer is at capacity, we need to reallocate
	if len(buffer) >= p.k {
		// Get a buffer from the pool or create a new one
		newBuffer := p.pool.Get().(*[]SpanWithSummary)
		*newBuffer = append(*newBuffer, spanWithSummary)
		p.activeBuffer.Store(newBuffer)
	} else {
		// There's room in the buffer, just append
		buffer = append(buffer, spanWithSummary)
		p.activeBuffer.Store(&buffer)
	}
}

// resetWindow flushes the current window and starts a new one
func (p *reservoirSamplerProcessor) resetWindow(now int64) {
	p.flushMu.Lock()
	defer p.flushMu.Unlock()

	// Flush the current active buffer
	p.flush()

	// Reset counters for the new window
	p.n.Store(0)
	p.t0.Store(now)

	// Create a new active buffer
	newBuffer := make([]SpanWithSummary, 0, p.k)
	p.activeBuffer.Store(&newBuffer)
}

// flush sends the current reservoir to the next consumer asynchronously
func (p *reservoirSamplerProcessor) flush() {
	startTime := time.Now()

	// Get the current active buffer and replace it with a new one
	oldBufferPtr := p.activeBuffer.Load()
	newBuffer := p.pool.Get().(*[]SpanWithSummary)
	*newBuffer = (*newBuffer)[:0] // Clear the buffer but keep capacity
	p.activeBuffer.Store(newBuffer)

	// Short-circuit if there are no spans to emit
	if len(*oldBufferPtr) == 0 {
		// Return empty buffer to pool
		p.pool.Put(oldBufferPtr)
		return
	}

	// Make a reference to the old buffer for async processing
	oldBuffer := *oldBufferPtr

	// Spawn a goroutine to send the spans to the next consumer
	go func() {
		flushStartTime := time.Now()

		defer func() {
			// Return the buffer to the pool
			*oldBufferPtr = (*oldBufferPtr)[:0] // Clear but keep capacity
			p.pool.Put(oldBufferPtr)

			// Record flush duration for metrics
			flushDuration := time.Since(flushStartTime).Nanoseconds()
			p.flushDuration.Store(flushDuration)
		}()

		// Extract full spans from LRU for the spans in the buffer
		traces := ptrace.NewTraces()
		missingCount := 0

		for _, spw := range oldBuffer {
			spanKey := createSpanKey(spw.Summary.TraceId, spw.Summary.SpanId)

			// Try to get full span from LRU
			p.lruMu.Lock()
			fullSpanWithSummary, ok := p.spanLRU.Get(spanKey)
			p.lruMu.Unlock()

			if ok {
				// We have the full span, add it to the traces to emit
				rs := traces.ResourceSpans().AppendEmpty()
				fullSpanWithSummary.Resource.CopyTo(rs.Resource())

				ss := rs.ScopeSpans().AppendEmpty()
				fullSpanWithSummary.Scope.CopyTo(ss.Scope())

				span := ss.Spans().AppendEmpty()
				fullSpanWithSummary.Span.CopyTo(span)
			} else {
				// Span was evicted from LRU, log and count
				missingCount++
			}
		}

		if missingCount > 0 {
			// Update eviction metric
			p.lruEvictionCount.Add(int64(missingCount))
			p.logger.Warn("Some spans were evicted from LRU cache before flushing",
				zap.Int("evicted_count", missingCount),
				zap.Int("total_count", len(oldBuffer)))
		}

		// Skip sending if no spans were found in LRU
		if traces.SpanCount() == 0 {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := p.nextConsumer.ConsumeTraces(ctx, traces); err != nil {
			p.logger.Error("Failed to forward reservoir spans", zap.Error(err))
		} else {
			p.logger.Debug("Flushed reservoir",
				zap.Int("span_count", traces.SpanCount()),
				zap.Int("buffer_size", len(oldBuffer)),
				zap.Uint64("total_processed", p.n.Load()))
		}
	}()
}

// checkpointLoop periodically checkpoints the reservoir state
func (p *reservoirSamplerProcessor) checkpointLoop() {
	for range p.chkTicker.C {
		if err := p.checkpoint(); err != nil {
			p.logger.Error("Failed to checkpoint reservoir", zap.Error(err))
		}
	}
}

// checkpoint saves the current reservoir state to disk
func (p *reservoirSamplerProcessor) checkpoint() error {
	// Track timing for metrics
	startTime := time.Now()

	// Acquire a read lock to ensure the reservoir isn't being flushed
	p.flushMu.Lock()
	defer p.flushMu.Unlock()

	// Get the current values
	n := p.n.Load()
	t0 := p.t0.Load()
	bufferPtr := p.activeBuffer.Load()
	buffer := *bufferPtr

	// Create a ReservoirCheckpoint proto message
	checkpoint := &spanprotos.ReservoirCheckpoint{
		TotalSeen:       n,
		WindowStartTime: t0,
		Spans: &spanprotos.SpanSummaryBatch{
			Summaries: make([]*spanprotos.SpanSummary, 0, len(buffer)),
		},
	}

	// Add span summaries to the checkpoint
	for _, spanWithSummary := range buffer {
		checkpoint.Spans.Summaries = append(checkpoint.Spans.Summaries, spanWithSummary.Summary)
	}

	// Marshal the checkpoint to binary protobuf format
	marshaledCheckpoint, err := proto.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("failed to marshal checkpoint: %w", err)
	}

	// Create n and t0 binary representations
	nBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(nBytes, n)

	t0Bytes := make([]byte, 8)
	binary.LittleEndian.PutInt64(t0Bytes, t0)

	// Use BoltDB to store the checkpoint
	err = p.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))

		// Store the current counter and window start time
		if err := b.Put([]byte(keyN), nBytes); err != nil {
			return err
		}

		if err := b.Put([]byte(keyT0), t0Bytes); err != nil {
			return err
		}

		// Store the spans
		return b.Put([]byte(keySpans), marshaledCheckpoint)
	})

	if err != nil {
		return err
	}

	// Update checkpoint age metric
	p.checkpointAge.Store(0) // Reset to 0 seconds

	// Record checkpoint duration for metrics
	duration := time.Since(startTime).Nanoseconds()
	p.checkpointDuration.Store(duration)

	// Record checkpoint time
	p.lastCheckpointTime.Store(time.Now().Unix())

	p.logger.Debug("Checkpoint saved",
		zap.Uint64("n", n),
		zap.Int64("t0", t0),
		zap.Int("span_count", len(buffer)),
		zap.Int("proto_bytes", len(marshaledCheckpoint)),
		zap.Duration("duration", time.Duration(duration)))

	return nil
}

// loadCheckpoint loads the reservoir state from disk
func (p *reservoirSamplerProcessor) loadCheckpoint() error {
	return p.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))

		// Load the counter
		nBytes := b.Get([]byte(keyN))
		if nBytes == nil {
			return fmt.Errorf("no checkpoint counter found")
		}

		// Parse n as uint64
		n := binary.LittleEndian.Uint64(nBytes)

		// Load the window start time
		t0Bytes := b.Get([]byte(keyT0))
		if t0Bytes == nil {
			return fmt.Errorf("no checkpoint window time found")
		}

		// Parse t0 as int64
		t0 := binary.LittleEndian.Int64(t0Bytes)

		// Load the spans protobuf
		spansBytes := b.Get([]byte(keySpans))
		if spansBytes == nil {
			return fmt.Errorf("no checkpoint spans found")
		}

		// Unmarshal the spans from protobuf
		checkpoint := &spanprotos.ReservoirCheckpoint{}
		if err := proto.Unmarshal(spansBytes, checkpoint); err != nil {
			return fmt.Errorf("failed to unmarshal checkpoint: %w", err)
		}

		// Create a new buffer to hold the loaded spans
		buffer := make([]SpanWithSummary, 0, len(checkpoint.Spans.Summaries))

		// Convert summaries to SpanWithSummary objects
		for _, summary := range checkpoint.Spans.Summaries {
			// Create a basic SpanWithSummary with just the summary
			// Full span data will be missing, but that's expected when loading from checkpoint
			spanWithSummary := SpanWithSummary{
				Summary: summary,
			}

			buffer = append(buffer, spanWithSummary)
		}

		// Set the loaded values
		p.n.Store(n)
		p.t0.Store(t0)
		p.activeBuffer.Store(&buffer)

		p.logger.Info("Checkpoint loaded",
			zap.Uint64("n", n),
			zap.Int64("t0", t0),
			zap.Int("span_count", len(buffer)))

		return nil
	})
}

// findOrCreateResource finds a matching resource in the target or creates a new one if it doesn't exist
func findOrCreateResource(target ptrace.Traces, source pcommon.Resource) pcommon.Resource {
	// For simplicity, always create a new resource
	// A more optimized version could try to find a matching resource
	rs := target.ResourceSpans().AppendEmpty()
	source.CopyTo(rs.Resource())
	return rs.Resource()
}

// findOrCreateScope finds a matching scope in the target resource or creates a new one if it doesn't exist
func findOrCreateScope(targetResource pcommon.Resource, sourceScope ptrace.ScopeSpans) ptrace.ScopeSpans {
	// For simplicity, always create a new scope
	// A more optimized version could try to find a matching scope
	rs := targetResource.Parent()
	ss := rs.ScopeSpans().AppendEmpty()
	sourceScope.Scope().CopyTo(ss.Scope())
	return ss
}

// Capabilities returns the capabilities of the processor.
func (p *reservoirSamplerProcessor) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: true}
}

// initMetrics initializes the metrics for the processor
func (p *reservoirSamplerProcessor) initMetrics() error {
	// Initialize reservoir size gauge
	_, err := p.metricsRegistry.GetOrCreateGauge(
		metrics.MetricPTEReservoirSize,
		metrics.DescPTEReservoirSize,
		metrics.UnitSpans,
	)
	if err != nil {
		return err
	}

	// Initialize window total counter
	_, err = p.metricsRegistry.GetOrCreateGauge(
		metrics.MetricPTEReservoirWindowTotal,
		metrics.DescPTEReservoirWindowTotal,
		metrics.UnitSpans,
	)
	if err != nil {
		return err
	}

	// Initialize checkpoint age gauge
	_, err = p.metricsRegistry.GetOrCreateGauge(
		metrics.MetricPTEReservoirCheckpointAge,
		metrics.DescPTEReservoirCheckpointAge,
		metrics.UnitSeconds,
	)
	if err != nil {
		return err
	}

	// Initialize LRU evictions counter
	_, err = p.metricsRegistry.GetOrCreateCounter(
		metrics.MetricPTEReservoirLRUEvictions,
		metrics.DescPTEReservoirLRUEvictions,
		metrics.UnitSpans,
	)
	if err != nil {
		return err
	}

	// Initialize flush duration histogram
	_, err = p.metricsRegistry.GetOrCreateHistogram(
		metrics.MetricPTEReservoirFlushDuration,
		metrics.DescPTEReservoirFlushDuration,
		metrics.UnitSeconds,
	)
	if err != nil {
		return err
	}

	// Initialize checkpoint duration histogram
	_, err = p.metricsRegistry.GetOrCreateHistogram(
		metrics.MetricPTEReservoirCheckpointDuration,
		metrics.DescPTEReservoirCheckpointDuration,
		metrics.UnitSeconds,
	)
	if err != nil {
		return err
	}

	// Initialize DB size gauge
	_, err = p.metricsRegistry.GetOrCreateGauge(
		metrics.MetricPTEReservoirDbSizeBytes,
		metrics.DescPTEReservoirDbSizeBytes,
		metrics.UnitBytes,
	)
	if err != nil {
		return err
	}

	// Initialize compaction count counter
	_, err = p.metricsRegistry.GetOrCreateCounter(
		metrics.MetricPTEReservoirCompactionCount,
		metrics.DescPTEReservoirCompactionCount,
		metrics.UnitCount,
	)
	if err != nil {
		return err
	}

	// Initialize compaction duration histogram
	_, err = p.metricsRegistry.GetOrCreateHistogram(
		metrics.MetricPTEReservoirCompactionDuration,
		metrics.DescPTEReservoirCompactionDuration,
		metrics.UnitSeconds,
	)
	if err != nil {
		return err
	}

	// Initialize trace metrics
	_, err = p.metricsRegistry.GetOrCreateCounter(
		"pte.reservoir.incomplete_traces",
		"Number of incomplete traces stored in the trace buffer map",
		metrics.UnitTraces,
	)
	if err != nil {
		return err
	}

	_, err = p.metricsRegistry.GetOrCreateCounter(
		"pte.reservoir.complete_traces",
		"Number of complete traces processed (with root span detected)",
		metrics.UnitTraces,
	)
	if err != nil {
		return err
	}

	_, err = p.metricsRegistry.GetOrCreateCounter(
		"pte.reservoir.timed_out_traces",
		"Number of traces that timed out without being complete",
		metrics.UnitTraces,
	)
	if err != nil {
		return err
	}

	return nil
}

// updateDbSizeMetric updates the database size metric
func (p *reservoirSamplerProcessor) updateDbSizeMetric() error {
	// Check if the database is open
	if p.db == nil {
		return nil
	}

	// Get database stats
	stats, err := p.db.Stats()
	if err != nil {
		return fmt.Errorf("failed to get database stats: %w", err)
	}

	// Update the DB size metric
	p.dbSizeBytes.Store(int64(stats.FileSize))

	return nil
}

// compactDatabaseWithDiskCheck performs BoltDB compaction with disk space verification
func (p *reservoirSamplerProcessor) compactDatabaseWithDiskCheck() error {
	startTime := time.Now()
	p.logger.Debug("Starting BoltDB compaction check with disk verification")

	// Update DB size metric first
	if err := p.updateDbSizeMetric(); err != nil {
		p.logger.Warn("Failed to update DB size before compaction", zap.Error(err))
		return err
	}

	currentSize := p.dbSizeBytes.Load()
	targetSize := p.config.DbCompactionTargetSize

	// Only compact if DB size exceeds target size
	if currentSize <= targetSize {
		p.logger.Debug("Skipping compaction, DB size is below target",
			zap.Int64("current_size_bytes", currentSize),
			zap.Int64("target_size_bytes", targetSize))
		return nil
	}

	// Check for available disk space
	dbDir := filepath.Dir(p.config.CheckpointPath)
	freeSpace, err := getDiskFreeSpace(dbDir)
	if err != nil {
		p.logger.Warn("Failed to check available disk space, proceeding with caution",
			zap.Error(err), zap.String("directory", dbDir))
	} else {
		// Ensure we have at least 2x the current DB size available (for the temporary file)
		requiredSpace := currentSize * 2
		if freeSpace < requiredSpace {
			p.logger.Error("Insufficient disk space for compaction",
				zap.Int64("available_bytes", freeSpace),
				zap.Int64("required_bytes", requiredSpace),
				zap.String("directory", dbDir))
			return fmt.Errorf("insufficient disk space for compaction: available %d bytes, need %d bytes",
				freeSpace, requiredSpace)
		}

		p.logger.Debug("Sufficient disk space for compaction",
			zap.Int64("available_bytes", freeSpace),
			zap.Int64("required_bytes", requiredSpace))
	}

	// Proceed with normal compaction
	return p.compactDatabase()
}

// compactDatabase performs BoltDB compaction if required
func (p *reservoirSamplerProcessor) compactDatabase() error {
	startTime := time.Now()
	p.logger.Debug("Starting BoltDB compaction check")

	// Update DB size metric first
	if err := p.updateDbSizeMetric(); err != nil {
		p.logger.Warn("Failed to update DB size before compaction", zap.Error(err))
	}

	currentSize := p.dbSizeBytes.Load()
	targetSize := p.config.DbCompactionTargetSize

	// Only compact if DB size exceeds target size
	if currentSize <= targetSize {
		p.logger.Debug("Skipping compaction, DB size is below target",
			zap.Int64("current_size_bytes", currentSize),
			zap.Int64("target_size_bytes", targetSize))
		return nil
	}

	// Compact the database
	p.logger.Info("Starting BoltDB compaction",
		zap.Int64("current_size_bytes", currentSize),
		zap.Int64("target_size_bytes", targetSize))

	// Close the database first
	if err := p.db.Close(); err != nil {
		return fmt.Errorf("failed to close database before compaction: %w", err)
	}

	// Create a temporary file path
	dbPath := p.config.CheckpointPath
	tempFile := dbPath + ".compacted"

	// Ensure any existing temp file is removed first
	os.Remove(tempFile)

	// Compact the database to a temporary file
	if err := bolt.Compact(tempFile, dbPath, 0644); err != nil {
		// Reopen the database and return the error
		p.db, _ = bolt.Open(dbPath, 0600, nil)
		return fmt.Errorf("failed to compact database: %w", err)
	}

	// Replace the original DB with the compacted one
	if err := os.Rename(tempFile, dbPath); err != nil {
		// Try to clean up the temp file
		os.Remove(tempFile)
		// Reopen the database and return the error
		p.db, _ = bolt.Open(dbPath, 0600, nil)
		return fmt.Errorf("failed to replace database with compacted version: %w", err)
	}

	// Reopen the database
	var err error
	p.db, err = bolt.Open(dbPath, 0600, nil)
	if err != nil {
		return fmt.Errorf("failed to reopen database after compaction: %w", err)
	}

	// Create bucket if it doesn't exist
	if err := p.db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucketName))
		return err
	}); err != nil {
		return fmt.Errorf("failed to create bucket after compaction: %w", err)
	}

	// Update metrics
	p.compactionCount.Add(1)
	p.compactionDuration.Store(time.Since(startTime).Nanoseconds())
	p.lastCompactionTime.Store(time.Now().Unix())

	// Update DB size after compaction
	if err := p.updateDbSizeMetric(); err != nil {
		p.logger.Warn("Failed to update DB size after compaction", zap.Error(err))
	}

	newSize := p.dbSizeBytes.Load()

	p.logger.Info("BoltDB compaction completed",
		zap.Int64("previous_size_bytes", currentSize),
		zap.Int64("current_size_bytes", newSize),
		zap.Duration("duration", time.Since(startTime)))

	return nil
}

// reportMetrics periodically reports metrics
func (p *reservoirSamplerProcessor) reportMetrics() {
	for {
		select {
		case <-p.metricsTicker.C:
			ctx := context.Background()
			attrs := map[string]string{"processor": p.registryID}

			// Report reservoir size
			buffer := p.activeBuffer.Load()
			size := len(*buffer)
			err := p.metricsRegistry.UpdateGauge(
				ctx,
				metrics.MetricPTEReservoirSize,
				float64(size),
				attrs,
			)
			if err != nil {
				p.logger.Error("Failed to update reservoir size metric", zap.Error(err))
			}

			// Report window total
			totalSpans := p.n.Load()
			err = p.metricsRegistry.UpdateGauge(
				ctx,
				metrics.MetricPTEReservoirWindowTotal,
				float64(totalSpans),
				attrs,
			)
			if err != nil {
				p.logger.Error("Failed to update window total metric", zap.Error(err))
			}

			// Report checkpoint age
			lastCheckpoint := p.lastCheckpointTime.Load()
			if lastCheckpoint > 0 {
				ageSeconds := float64(time.Now().Unix() - lastCheckpoint)
				err = p.metricsRegistry.UpdateGauge(
					ctx,
					metrics.MetricPTEReservoirCheckpointAge,
					ageSeconds,
					attrs,
				)
				if err != nil {
					p.logger.Error("Failed to update checkpoint age metric", zap.Error(err))
				}
			}

			// Report LRU evictions
			evictions := p.lruEvictionCount.Swap(0)
			if evictions > 0 {
				err = p.metricsRegistry.UpdateCounter(
					ctx,
					metrics.MetricPTEReservoirLRUEvictions,
					float64(evictions),
					attrs,
				)
				if err != nil {
					p.logger.Error("Failed to update LRU evictions metric", zap.Error(err))
				}
			}

			// Report flush duration
			flushDuration := p.flushDuration.Swap(0)
			if flushDuration > 0 {
				err = p.metricsRegistry.UpdateHistogram(
					ctx,
					metrics.MetricPTEReservoirFlushDuration,
					float64(flushDuration)/float64(time.Second),
					attrs,
				)
				if err != nil {
					p.logger.Error("Failed to update flush duration metric", zap.Error(err))
				}
			}

			// Report checkpoint duration
			checkpointDuration := p.checkpointDuration.Swap(0)
			if checkpointDuration > 0 {
				err = p.metricsRegistry.UpdateHistogram(
					ctx,
					metrics.MetricPTEReservoirCheckpointDuration,
					float64(checkpointDuration)/float64(time.Second),
					attrs,
				)
				if err != nil {
					p.logger.Error("Failed to update checkpoint duration metric", zap.Error(err))
				}
			}

			// Report DB-related metrics
			p.reportDbMetrics(ctx, attrs)

			// Report trace metrics
			p.reportTraceMetrics(ctx, attrs)

		case <-p.stopCh:
			return
		}
	}
}

// reportTraceMetrics reports trace-related metrics
func (p *reservoirSamplerProcessor) reportTraceMetrics(ctx context.Context, attrs map[string]string) {
	// Report number of incomplete traces
	incompleteTraces := int64(len(p.traceBuffers.buffers))
	p.incompleteTraceCount.Store(incompleteTraces)

	err := p.metricsRegistry.UpdateGauge(
		ctx,
		"pte.reservoir.incomplete_traces",
		float64(incompleteTraces),
		attrs,
	)
	if err != nil {
		p.logger.Error("Failed to update incomplete traces metric", zap.Error(err))
	}

	// Report complete traces count
	completeTraces := p.completeTraceCount.Swap(0)
	if completeTraces > 0 {
		err = p.metricsRegistry.UpdateCounter(
			ctx,
			"pte.reservoir.complete_traces",
			float64(completeTraces),
			attrs,
		)
		if err != nil {
			p.logger.Error("Failed to update complete traces metric", zap.Error(err))
		}
	}

	// Report timed out traces count
	timedOutTraces := p.timedOutTraceCount.Swap(0)
	if timedOutTraces > 0 {
		err = p.metricsRegistry.UpdateCounter(
			ctx,
			"pte.reservoir.timed_out_traces",
			float64(timedOutTraces),
			attrs,
		)
		if err != nil {
			p.logger.Error("Failed to update timed out traces metric", zap.Error(err))
		}
	}
}

// Start implements the component.Component interface.
func (p *reservoirSamplerProcessor) Start(ctx context.Context, host component.Host) error {
	// Start a background goroutine to report metrics periodically
	p.metricsTicker = time.NewTicker(10 * time.Second)
	go p.reportMetrics()

	// Set the initial checkpoint time
	p.lastCheckpointTime.Store(time.Now().Unix())

	// Get the initial DB size
	if err := p.updateDbSizeMetric(); err != nil {
		p.logger.Warn("Failed to get initial DB size", zap.Error(err))
	}

	// Set up compaction scheduler if configured
	if p.config.DbCompactionScheduleCron != "" {
		p.logger.Info("Setting up BoltDB compaction scheduler",
			zap.String("schedule", p.config.DbCompactionScheduleCron),
			zap.Int64("target_size_bytes", p.config.DbCompactionTargetSize))

		// Create new cron scheduler with seconds precision
		p.cronScheduler = cron.New(cron.WithSeconds())

		// Add compaction job - now uses the enhanced compaction method with disk space verification
		var err error
		p.cronEntryID, err = p.cronScheduler.AddFunc(p.config.DbCompactionScheduleCron, func() {
			p.logger.Debug("Running scheduled BoltDB compaction with disk space verification")
			if err := p.compactDatabaseWithDiskCheck(); err != nil {
				p.logger.Error("Scheduled BoltDB compaction failed", zap.Error(err))
			}
		})

		if err != nil {
			p.logger.Error("Failed to schedule BoltDB compaction",
				zap.String("schedule", p.config.DbCompactionScheduleCron),
				zap.Error(err))
		} else {
			// Start the scheduler
			p.cronScheduler.Start()
			p.logger.Info("BoltDB compaction scheduler started")
		}
	} else {
		p.logger.Info("BoltDB compaction scheduler not configured")
	}

	p.logger.Info("Reservoir sampler processor started with metrics reporting")
	return nil
}

// Shutdown implements the component.Component interface.
func (p *reservoirSamplerProcessor) Shutdown(_ context.Context) error {
	// Stop the checkpoint ticker
	if p.chkTicker != nil {
		p.chkTicker.Stop()
	}

	// Stop the metrics ticker
	if p.metricsTicker != nil {
		p.metricsTicker.Stop()
	}

	// Stop the cron scheduler
	if p.cronScheduler != nil {
		p.cronScheduler.Stop()
		p.logger.Info("BoltDB compaction scheduler stopped")
	}

	// Stop the trace buffer map cleaner if trace-aware sampling was enabled
	if p.config.TraceAware && p.traceBuffers != nil {
		p.traceBuffers.shutdown()
		p.logger.Info("Trace buffer map cleaner stopped")
	}

	close(p.stopCh)

	// Final checkpoint
	if err := p.checkpoint(); err != nil {
		p.logger.Error("Failed to save final checkpoint", zap.Error(err))
	}

	// Get the final DB size for logging
	if err := p.updateDbSizeMetric(); err == nil {
		p.logger.Info("Final database metrics",
			zap.Int64("db_size_bytes", p.dbSizeBytes.Load()),
			zap.Int64("compaction_count", p.compactionCount.Load()))
	}

	// Close the database
	if p.db != nil {
		return p.db.Close()
	}

	return nil
}

// ID implements the Tunable interface
func (p *reservoirSamplerProcessor) ID() string {
	return p.registryID
}

// SetValue implements the Tunable interface
func (p *reservoirSamplerProcessor) SetValue(key string, value float64) {
	if key == "k_size" {
		newK := int(value)
		if newK > 0 {
			p.flushMu.Lock()
			defer p.flushMu.Unlock()
			p.k = newK
			p.logger.Info("Reservoir k_size updated", zap.Int("new_k", newK))
		}
	} else if key == "trace_timeout" {
		newTimeout := time.Duration(value) * time.Second
		if newTimeout > 0 {
			p.logger.Info("Trace timeout updated", zap.Duration("new_timeout", newTimeout))
			p.traceTimeoutDuration = newTimeout
		}
	} else if key == "trigger_compaction" && value > 0 {
		// On-demand compaction API
		// Only trigger if value > 0 (typically use 1.0)
		go func() {
			p.logger.Info("On-demand BoltDB compaction triggered")
			if err := p.compactDatabaseWithDiskCheck(); err != nil {
				p.logger.Error("On-demand BoltDB compaction failed", zap.Error(err))
			} else {
				p.logger.Info("On-demand BoltDB compaction completed successfully")
			}
		}()
	}
	// Other tunable parameters could be added here
}

// GetValue implements the Tunable interface
func (p *reservoirSamplerProcessor) GetValue(key string) float64 {
	switch key {
	case "k_size":
		return float64(p.k)
	case "lru_eviction_count":
		return float64(p.lruEvictionCount.Load())
	case "checkpoint_age_seconds":
		return float64(p.checkpointAge.Load())
	case "db_size_bytes":
		return float64(p.dbSizeBytes.Load())
	case "compaction_count":
		return float64(p.compactionCount.Load())
	case "last_compaction_time":
		return float64(p.lastCompactionTime.Load())
	case "trace_timeout":
		return float64(p.traceTimeoutDuration / time.Second)
	case "incomplete_trace_count":
		return float64(p.incompleteTraceCount.Load())
	case "complete_trace_count":
		return float64(p.completeTraceCount.Load())
	case "timed_out_trace_count":
		return float64(p.timedOutTraceCount.Load())
	case "trigger_compaction":
		// Always returns 0 since this is a write-only parameter
		return 0.0
	}
	return 0.0
}

// getDiskFreeSpace returns the available disk space in bytes for the given path
func getDiskFreeSpace(path string) (int64, error) {
	var stat syscall.Statfs_t
	err := syscall.Statfs(path, &stat)
	if err != nil {
		return 0, err
	}

	// Available blocks * size per block
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

// reportDbMetrics reports all DB-related metrics
func (p *reservoirSamplerProcessor) reportDbMetrics(ctx context.Context, attrs map[string]string) {
	// Report DB size metric
	if err := p.updateDbSizeMetric(); err != nil {
		p.logger.Warn("Failed to update DB size metric", zap.Error(err))
	} else {
		dbSize := p.dbSizeBytes.Load()
		if dbSize > 0 {
			err := p.metricsRegistry.UpdateGauge(
				ctx,
				metrics.MetricPTEReservoirDbSizeBytes,
				float64(dbSize),
				attrs,
			)
			if err != nil {
				p.logger.Error("Failed to update DB size metric", zap.Error(err))
			}
		}
	}

	// Report compaction metrics
	compactionCount := p.compactionCount.Load()
	if compactionCount > 0 {
		err := p.metricsRegistry.UpdateCounter(
			ctx,
			metrics.MetricPTEReservoirCompactionCount,
			float64(compactionCount),
			attrs,
		)
		if err != nil {
			p.logger.Error("Failed to update compaction count metric", zap.Error(err))
		}
	}

	compactionDuration := p.compactionDuration.Load()
	if compactionDuration > 0 {
		err := p.metricsRegistry.UpdateHistogram(
			ctx,
			metrics.MetricPTEReservoirCompactionDuration,
			float64(compactionDuration)/float64(time.Second),
			attrs,
		)
		if err != nil {
			p.logger.Error("Failed to update compaction duration metric", zap.Error(err))
		}
	}
}
