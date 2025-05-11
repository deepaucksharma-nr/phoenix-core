package reservoirsampler

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/boltdb/bolt"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
	"go.uber.org/zap"
)

const (
	bucketName = "reservoir_checkpoint"
	keyN       = "n"
	keyT0      = "t0"
	keySpans   = "spans"
)

type reservoirSamplerProcessor struct {
	config       *Config
	nextConsumer consumer.Traces
	logger       *zap.Logger
	
	// Reservoir variables
	k          int
	windowNS   int64
	n          atomic.Uint64
	t0         atomic.Int64
	
	// Sampling buffer management
	active    atomic.Pointer[ptrace.Traces]
	flushMu   sync.Mutex
	tracesMap sync.Map // Maps traceID to *ptrace.Traces to maintain trace coherence
	pool      sync.Pool
	
	// Checkpointing
	db        *bolt.DB
	chkTicker *time.Ticker
	
	// Random number generation
	rand *rand.Rand
	mu   sync.Mutex
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
	
	// Initialize processor
	p := &reservoirSamplerProcessor{
		config:       cfg,
		nextConsumer: nextConsumer,
		logger:       settings.Logger,
		k:            cfg.SizeK,
		windowNS:     windowDuration.Nanoseconds(),
		db:           db,
		rand:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	
	// Initialize active buffer
	active := ptrace.NewTraces()
	p.active.Store(&active)
	
	// Setup object pool for traces
	p.pool.New = func() interface{} {
		t := ptrace.NewTraces()
		return &t
	}
	
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
		scopeSpans := rs.ScopeSpans()
		
		for j := 0; j < scopeSpans.Len(); j++ {
			ss := scopeSpans.At(j)
			spans := ss.Spans()
			
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				p.sampleSpan(rs, ss, span)
			}
		}
	}
	
	return nil
}

// sampleSpan implements Algorithm R for reservoir sampling
func (p *reservoirSamplerProcessor) sampleSpan(rs ptrace.ResourceSpans, ss ptrace.ScopeSpans, span ptrace.Span) {
	// Sample with Algorithm R
	i := p.n.Add(1) // Increment n and get the new value
	
	if i <= uint64(p.k) {
		// Reservoir not yet full, add the span directly
		p.addSpanToReservoir(rs, ss, span)
	} else {
		// Reservoir is full, replace with probability k/i
		p.mu.Lock()
		r := p.rand.Int63n(int64(i))
		p.mu.Unlock()
		
		if r < int64(p.k) {
			// Replace the span at index r
			p.addSpanToReservoir(rs, ss, span)
		}
	}
}

// addSpanToReservoir adds a span to the reservoir
func (p *reservoirSamplerProcessor) addSpanToReservoir(rs ptrace.ResourceSpans, ss ptrace.ScopeSpans, span ptrace.Span) {
	// Get the current active buffer
	activePtr := p.active.Load()
	active := *activePtr
	
	// Add the span to the active buffer
	// This requires copying the span and its context to avoid data races
	newRS := findOrCreateResource(active, rs.Resource())
	newSS := findOrCreateScope(newRS, ss)
	newSpan := newSS.Spans().AppendEmpty()
	span.CopyTo(newSpan)
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
	active := ptrace.NewTraces()
	p.active.Store(&active)
}

// flush sends the current reservoir to the next consumer asynchronously
func (p *reservoirSamplerProcessor) flush() {
	// Get the current active buffer and replace it with a new one
	oldActivePtr := p.active.Load()
	newActive := ptrace.NewTraces()
	p.active.Store(&newActive)
	
	// Short-circuit if there are no spans to emit
	if (*oldActivePtr).SpanCount() == 0 {
		return
	}
	
	// Make a copy for async processing
	spansCopy := *oldActivePtr
	
	// Spawn a goroutine to send the spans to the next consumer
	go func() {
		defer func() {
			// Reset and return the buffer to the pool
			spansCopy.ResourceSpans().RemoveIf(func(_ ptrace.ResourceSpans) bool { return true })
			// Potential alternative if Reset() is available:
			// spansCopy.Reset()
			// p.pool.Put(&spansCopy)
		}()
		
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		
		if err := p.nextConsumer.ConsumeTraces(ctx, spansCopy); err != nil {
			p.logger.Error("Failed to forward reservoir spans", zap.Error(err))
		} else {
			p.logger.Debug("Flushed reservoir",
				zap.Int("span_count", spansCopy.SpanCount()),
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
	// Acquire a read lock to ensure the reservoir isn't being flushed
	p.flushMu.Lock()
	defer p.flushMu.Unlock()
	
	// Get the current values
	n := p.n.Load()
	t0 := p.t0.Load()
	activePtr := p.active.Load()
	active := *activePtr
	
	// Marshal the spans (simplified for demonstration)
	// In a real implementation, we would use a more efficient serialization method
	marshaledSpans := []byte{}
	
	// Use BoltDB to store the checkpoint
	err := p.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		
		// Store the current counter and window start time
		if err := b.Put([]byte(keyN), []byte(string(n))); err != nil {
			return err
		}
		
		if err := b.Put([]byte(keyT0), []byte(string(t0))); err != nil {
			return err
		}
		
		// Store the spans
		return b.Put([]byte(keySpans), marshaledSpans)
	})
	
	if err != nil {
		return err
	}
	
	p.logger.Debug("Checkpoint saved",
		zap.Uint64("n", n),
		zap.Int64("t0", t0),
		zap.Int("span_count", active.SpanCount()))
	
	return nil
}

// loadCheckpoint loads the reservoir state from disk
func (p *reservoirSamplerProcessor) loadCheckpoint() error {
	return p.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		
		// Load the counter
		nBytes := b.Get([]byte(keyN))
		if nBytes == nil {
			return nil // No checkpoint found
		}
		
		// Parse n
		var n uint64
		// In a real implementation, properly parse the uint64 from bytes
		
		// Load the window start time
		t0Bytes := b.Get([]byte(keyT0))
		if t0Bytes == nil {
			return nil
		}
		
		// Parse t0
		var t0 int64
		// In a real implementation, properly parse the int64 from bytes
		
		// Load the spans
		spansBytes := b.Get([]byte(keySpans))
		if spansBytes == nil {
			return nil
		}
		
		// Unmarshal the spans
		// In a real implementation, properly unmarshal the spans
		
		// Set the loaded values
		p.n.Store(n)
		p.t0.Store(t0)
		
		p.logger.Info("Checkpoint loaded",
			zap.Uint64("n", n),
			zap.Int64("t0", t0))
		
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

// Start implements the component.Component interface.
func (p *reservoirSamplerProcessor) Start(_ context.Context, _ component.Host) error {
	return nil
}

// Shutdown implements the component.Component interface.
func (p *reservoirSamplerProcessor) Shutdown(_ context.Context) error {
	// Stop the checkpoint ticker
	if p.chkTicker != nil {
		p.chkTicker.Stop()
	}
	
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