package adaptiveheadsampler

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deepaucksharma-nr/phoenix-core/internal/pkg/samplerregistry"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
	"go.uber.org/zap"
)

type adaptiveHeadSamplerProcessor struct {
	config        *Config
	nextConsumer  consumer.Traces
	logger        *zap.Logger
	currentP      atomic.Float64
	logThrottle   atomic.Int64 // Timestamp of last log to throttle logging
	hasher        func([]byte) uint64
	registryID    string
	traceIDRand   *rand.Rand
	traceIDRandMu sync.Mutex
}

// Ensure adaptiveHeadSamplerProcessor implements required interfaces
var _ processor.Traces = (*adaptiveHeadSamplerProcessor)(nil)

// newProcessor creates a new head sampling processor.
func newProcessor(settings processor.CreateSettings, config component.Config, nextConsumer consumer.Traces) (processor.Traces, error) {
	cfg := config.(*Config)
	
	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	p := &adaptiveHeadSamplerProcessor{
		config:       cfg,
		nextConsumer: nextConsumer,
		logger:       settings.Logger,
		registryID:   "adaptive_head_sampler",
		traceIDRand:  rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	
	// Set the initial probability
	p.currentP.Store(cfg.InitialProbability)
	
	// Configure the hasher based on the hash seed config
	if cfg.HashSeedConfig == "XORTraceID" {
		p.hasher = p.xorTraceIDHasher
	} else {
		p.hasher = p.recordIDHasher
	}
	
	// Register with the registry
	registry := samplerregistry.GetInstance()
	registry.Register(p.registryID, p)
	
	p.logger.Info("Adaptive head sampler processor created",
		zap.Float64("initial_probability", cfg.InitialProbability),
		zap.Float64("min_p", cfg.MinP),
		zap.Float64("max_p", cfg.MaxP),
		zap.String("hash_seed_config", cfg.HashSeedConfig))
	
	return p, nil
}

// SetProbability updates the sampling probability
func (p *adaptiveHeadSamplerProcessor) SetProbability(newP float64) {
	// Clamp the probability to the configured min/max
	if newP < p.config.MinP {
		newP = p.config.MinP
	}
	if newP > p.config.MaxP {
		newP = p.config.MaxP
	}
	
	// Only log if the change is significant (>0.01) and we haven't logged recently
	oldP := p.currentP.Load()
	delta := newP - oldP
	if delta < 0 {
		delta = -delta
	}
	
	// Store the new probability regardless of whether we log
	p.currentP.Store(newP)
	
	// Throttle logging to avoid spamming logs
	if delta > 0.01 {
		now := time.Now().Unix()
		lastLog := p.logThrottle.Load()
		
		// Log at most once per 15 seconds
		if now - lastLog > 15 {
			p.logThrottle.Store(now)
			p.logger.Info("Adaptive head sampler probability changed",
				zap.Float64("old_p", oldP),
				zap.Float64("new_p", newP))
		}
	}
}

// GetProbability returns the current sampling probability
func (p *adaptiveHeadSamplerProcessor) GetProbability() float64 {
	return p.currentP.Load()
}

// ConsumeTraces implements the processor.Traces interface
func (p *adaptiveHeadSamplerProcessor) ConsumeTraces(ctx context.Context, traces ptrace.Traces) error {
	currentP := p.currentP.Load()
	if currentP >= 1.0 {
		// Fast path: if sampling at 100%, just pass through all traces
		return p.nextConsumer.ConsumeTraces(ctx, traces)
	}
	
	// Create a new Traces instance to store the sampled traces
	sampledTraces := ptrace.NewTraces()
	
	resourceSpans := traces.ResourceSpans()
	for i := 0; i < resourceSpans.Len(); i++ {
		rs := resourceSpans.At(i)
		scopeSpans := rs.ScopeSpans()
		
		for j := 0; j < scopeSpans.Len(); j++ {
			ss := scopeSpans.At(j)
			spans := ss.Spans()
			
			// Track which traces we've already decided on
			decisions := make(map[pcommon.TraceID]bool)
			
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				traceID := span.TraceID()
				
				// If we've already made a decision for this trace, apply it
				if decision, exists := decisions[traceID]; exists {
					if !decision {
						continue // Skip this span
					}
				} else {
					// Make a new decision for this trace
					decision := p.shouldSample(traceID)
					decisions[traceID] = decision
					if !decision {
						continue // Skip this span
					}
				}
				
				// Add the span to the sampled traces
				// This requires creating the resource and scope hierarchies if needed
				rs := findOrCreateResource(sampledTraces, rs)
				ss := findOrCreateScope(rs, ss)
				newSpan := ss.Spans().AppendEmpty()
				span.CopyTo(newSpan)
			}
		}
	}
	
	// If no spans were sampled, don't bother sending it forward
	if sampledTraces.SpanCount() == 0 {
		return nil
	}
	
	return p.nextConsumer.ConsumeTraces(ctx, sampledTraces)
}

// shouldSample determines if a trace should be sampled based on the trace ID and current probability
func (p *adaptiveHeadSamplerProcessor) shouldSample(traceID pcommon.TraceID) bool {
	hash := p.hasher(traceID[:])
	
	// Convert hash to a value between 0 and 1
	// Using a thread-safe random number generator if needed
	var randVal float64
	if p.config.HashSeedConfig == "XORTraceID" {
		// Deterministic: use the hash directly
		randVal = float64(hash) / float64(^uint64(0))
	} else {
		// Non-deterministic: use the hash as a seed for random
		p.traceIDRandMu.Lock()
		p.traceIDRand.Seed(int64(hash))
		randVal = p.traceIDRand.Float64()
		p.traceIDRandMu.Unlock()
	}
	
	return randVal < p.currentP.Load()
}

// xorTraceIDHasher creates a 64-bit hash from a trace ID by XORing its high and low parts
func (p *adaptiveHeadSamplerProcessor) xorTraceIDHasher(id []byte) uint64 {
	if len(id) != 16 {
		// Should never happen with valid trace IDs
		return 0
	}
	
	// Extract high and low 64-bit parts and XOR them
	high := binary.BigEndian.Uint64(id[0:8])
	low := binary.BigEndian.Uint64(id[8:16])
	return high ^ low
}

// recordIDHasher hashes the record ID fields (for logs)
func (p *adaptiveHeadSamplerProcessor) recordIDHasher(id []byte) uint64 {
	// For trace IDs, just use a simple hash
	if len(id) == 16 {
		return p.xorTraceIDHasher(id)
	}
	
	// For record IDs (strings), use SHA1
	h := sha1.New()
	h.Write(id)
	sum := h.Sum(nil)
	return binary.BigEndian.Uint64(sum[0:8])
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
func (p *adaptiveHeadSamplerProcessor) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: true}
}

// Start implements the component.Component interface.
func (p *adaptiveHeadSamplerProcessor) Start(context.Context, component.Host) error {
	return nil
}

// Shutdown implements the component.Component interface.
func (p *adaptiveHeadSamplerProcessor) Shutdown(context.Context) error {
	return nil
}