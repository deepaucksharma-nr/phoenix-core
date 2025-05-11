package reservoirsampler

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/boltdb/bolt"
	"github.com/deepaucksharma-nr/phoenix-core/internal/metrics"
	"github.com/deepaucksharma-nr/phoenix-core/internal/pkg/tunableregistry"
	"github.com/deepaucksharma-nr/phoenix-core/internal/processor/reservoirsampler/spanprotos"
	"github.com/hashicorp/golang-lru/v2/simplelru"
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
)

// SpanWithSummary pairs a span summary with its full span data
type SpanWithSummary struct {
	Summary *spanprotos.SpanSummary
	Span    ptrace.Span
	Resource pcommon.Resource
	Scope    pcommon.InstrumentationScope
}

type reservoirSamplerProcessor struct {
	config       *Config
	nextConsumer consumer.Traces
	logger       *zap.Logger
	registryID   string

	// Reservoir variables
	k          int
	windowNS   int64
	n          atomic.Uint64
	t0         atomic.Int64

	// Sampling buffer management
	activeBuffer atomic.Pointer[[]SpanWithSummary]
	flushMu      sync.Mutex
	pool         sync.Pool

	// LRU cache for full spans
	spanLRU      *simplelru.LRU[string, SpanWithSummary]
	lruMu        sync.Mutex

	// Checkpointing
	db        *bolt.DB
	chkTicker *time.Ticker

	// Random number generation
	rand *rand.Rand
	mu   sync.Mutex

	// Metrics
	metricsRegistry       *metrics.MetricsRegistry
	metricsTicker         *time.Ticker
	stopCh                chan struct{}
	lruEvictionCount      atomic.Int64
	checkpointAge         atomic.Int64
	flushDuration         atomic.Int64
	checkpointDuration    atomic.Int64
	lastCheckpointTime    atomic.Int64
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

	// Create LRU cache for full span data (2x size of reservoir to allow some buffer)
	onEvict := func(key string, value SpanWithSummary) {
		// No need to use atomics for the processor struct reference,
		// but we need to use atomics for incrementing the counter
		processor, ok := interface{}(p).(*reservoirSamplerProcessor)
		if ok {
			processor.lruEvictionCount.Add(1)
		}
	}
	lruCache, err := simplelru.NewLRU[string, SpanWithSummary](cfg.SizeK*2, onEvict)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create LRU cache: %w", err)
	}

	// Initialize processor
	p := &reservoirSamplerProcessor{
		config:          cfg,
		nextConsumer:    nextConsumer,
		logger:          settings.Logger,
		registryID:      "reservoir_sampler",
		k:               cfg.SizeK,
		windowNS:        windowDuration.Nanoseconds(),
		db:              db,
		rand:            rand.New(rand.NewSource(time.Now().UnixNano())),
		spanLRU:         lruCache,
		metricsRegistry: metrics.GetInstance(settings.Logger),
		stopCh:          make(chan struct{}),
	}

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
		zap.String("checkpoint_interval", cfg.CheckpointInterval))

	return p, nil
}

// ConsumeTraces implements the processor.Traces interface
func (p *reservoirSamplerProcessor) ConsumeTraces(ctx context.Context, traces ptrace.Traces) error {
	// Check if window has expired
	now := time.Now().UnixNano()
	windowStart := p.t0.Load()

	if now - windowStart > p.windowNS {
		// Window has expired, flush current reservoir and start a new one
		p.resetWindow(now)
	}

	// Process the incoming trace data for reservoir sampling
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
				p.sampleSpan(span, resource, scope)
			}
		}
	}

	return nil
}

// sampleSpan implements Algorithm R for reservoir sampling
func (p *reservoirSamplerProcessor) sampleSpan(
	span ptrace.Span,
	resource pcommon.Resource,
	scope pcommon.InstrumentationScope,
) {
	// Sample with Algorithm R
	i := p.n.Add(1) // Increment n and get the new value

	if i <= uint64(p.k) {
		// Reservoir not yet full, add the span directly
		p.addSpanToReservoir(span, resource, scope)
	} else {
		// Reservoir is full, replace with probability k/i
		p.mu.Lock()
		r := p.rand.Int63n(int64(i))
		p.mu.Unlock()

		if r < int64(p.k) {
			// Replace the span at index r
			p.addSpanToReservoir(span, resource, scope)
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

	// Create span with summary
	spanWithSummary := SpanWithSummary{
		Summary:  summary,
		Span:     span,
		Resource: resource,
		Scope:    scope,
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
		TotalSeen:      n,
		WindowStartTime: t0,
		Spans:          &spanprotos.SpanSummaryBatch{
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
					float64(flushDuration) / float64(time.Second),
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
					float64(checkpointDuration) / float64(time.Second),
					attrs,
				)
				if err != nil {
					p.logger.Error("Failed to update checkpoint duration metric", zap.Error(err))
				}
			}

		case <-p.stopCh:
			return
		}
	}
}

// Start implements the component.Component interface.
func (p *reservoirSamplerProcessor) Start(_ context.Context, _ component.Host) error {
	// Start a background goroutine to report metrics periodically
	p.metricsTicker = time.NewTicker(10 * time.Second)
	go p.reportMetrics()

	// Set the initial checkpoint time
	p.lastCheckpointTime.Store(time.Now().Unix())

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

	close(p.stopCh)

	// Final checkpoint
	if err := p.checkpoint(); err != nil {
		p.logger.Error("Failed to save final checkpoint", zap.Error(err))
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
	}
	return 0.0
}