package adaptiveheadsampler

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/deepaucksharma-nr/phoenix-core/internal/metrics"
	"github.com/deepaucksharma-nr/phoenix-core/internal/pkg/tunableregistry"
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

	// Metrics
	metricsRegistry *metrics.MetricsRegistry
	rejectedCount   atomic.Int64
	processingCount atomic.Int64
	metricsTicker   *time.Ticker
	stopCh          chan struct{}
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
		config:          cfg,
		nextConsumer:    nextConsumer,
		logger:          settings.Logger,
		registryID:      "adaptive_head_sampler",
		traceIDRand:     rand.New(rand.NewSource(time.Now().UnixNano())),
		metricsRegistry: metrics.GetInstance(settings.Logger),
		stopCh:          make(chan struct{}),
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
	registry := tunableregistry.GetInstance()
	registry.Register(p)

	// Initialize metrics - create them but don't start reporting yet
	if err := p.initMetrics(); err != nil {
		return nil, fmt.Errorf("failed to initialize metrics: %w", err)
	}

	p.logger.Info("Adaptive head sampler processor created",
		zap.Float64("initial_probability", cfg.InitialProbability),
		zap.Float64("min_p", cfg.MinP),
		zap.Float64("max_p", cfg.MaxP),
		zap.String("hash_seed_config", cfg.HashSeedConfig))

	return p, nil
}

// initMetrics initializes the metrics for the processor
func (p *adaptiveHeadSamplerProcessor) initMetrics() error {
	// Initialize sampling probability gauge
	_, err := p.metricsRegistry.GetOrCreateGauge(
		metrics.MetricPTEHeadSamplingProbability,
		metrics.DescPTEHeadSamplingProbability,
		metrics.UnitRatio,
	)
	if err != nil {
		return err
	}

	// Initialize throughput gauge
	_, err = p.metricsRegistry.GetOrCreateGauge(
		metrics.MetricPTEHeadSamplingThroughput,
		metrics.DescPTEHeadSamplingThroughput,
		metrics.UnitSpans,
	)
	if err != nil {
		return err
	}

	// Initialize rejected spans counter
	_, err = p.metricsRegistry.GetOrCreateCounter(
		metrics.MetricPTEHeadSamplingRejected,
		metrics.DescPTEHeadSamplingRejected,
		metrics.UnitSpans,
	)
	if err != nil {
		return err
	}

	return nil
}

// ID implements the Tunable interface
func (p *adaptiveHeadSamplerProcessor) ID() string {
	return p.registryID
}

// SetValue implements the Tunable interface
func (p *adaptiveHeadSamplerProcessor) SetValue(key string, value float64) {
	if key == "probability" {
		p.setProbability(value)
	}
	// Ignore other keys for now
}

// GetValue implements the Tunable interface
func (p *adaptiveHeadSamplerProcessor) GetValue(key string) float64 {
	if key == "probability" {
		return p.currentP.Load()
	}
	// Return 0 for unknown keys
	return 0
}

// setProbability updates the sampling probability (internal implementation)
func (p *adaptiveHeadSamplerProcessor) setProbability(newP float64) {
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

// SetProbability updates the sampling probability (backward compatibility)
func (p *adaptiveHeadSamplerProcessor) SetProbability(newP float64) {
	p.SetValue("probability", newP)
}

// GetProbability returns the current sampling probability (backward compatibility)
func (p *adaptiveHeadSamplerProcessor) GetProbability() float64 {
	return p.GetValue("probability")
}

// ConsumeTraces implements the processor.Traces interface
func (p *adaptiveHeadSamplerProcessor) ConsumeTraces(ctx context.Context, traces ptrace.Traces) error {
	// Track counts for metrics
	totalSpans := traces.SpanCount()
	p.processingCount.Add(int64(totalSpans))

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

	// Count rejected spans for metrics
	sampledCount := sampledTraces.SpanCount()
	rejectedCount := totalSpans - sampledCount
	if rejectedCount > 0 {
		p.rejectedCount.Add(int64(rejectedCount))
	}

	// If no spans were sampled, don't bother sending it forward
	if sampledCount == 0 {
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

	// For record IDs (strings), use xxHash (much faster than SHA1)
	return xxhash.Sum64(id)
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
func (p *adaptiveHeadSamplerProcessor) Start(ctx context.Context, host component.Host) error {
	// Start a background goroutine to report metrics periodically
	p.metricsTicker = time.NewTicker(10 * time.Second)
	go p.reportMetrics()

	p.logger.Info("Adaptive head sampler processor started with metrics reporting")
	return nil
}

// Shutdown implements the component.Component interface.
func (p *adaptiveHeadSamplerProcessor) Shutdown(ctx context.Context) error {
	if p.metricsTicker != nil {
		p.metricsTicker.Stop()
	}

	close(p.stopCh)
	return nil
}

// reportMetrics periodically reports metrics
func (p *adaptiveHeadSamplerProcessor) reportMetrics() {
	for {
		select {
		case <-p.metricsTicker.C:
			// Report current probability
			err := p.metricsRegistry.UpdateGauge(
				context.Background(),
				metrics.MetricPTEHeadSamplingProbability,
				p.currentP.Load(),
				map[string]string{"processor": p.registryID},
			)
			if err != nil {
				p.logger.Error("Failed to update probability metric", zap.Error(err))
			}

			// Report throughput (spans per second)
			count := p.processingCount.Swap(0)
			throughput := float64(count) / 10.0 // 10 second interval
			err = p.metricsRegistry.UpdateGauge(
				context.Background(),
				metrics.MetricPTEHeadSamplingThroughput,
				throughput,
				map[string]string{"processor": p.registryID},
			)
			if err != nil {
				p.logger.Error("Failed to update throughput metric", zap.Error(err))
			}

			// Report rejected spans
			rejected := p.rejectedCount.Swap(0)
			if rejected > 0 {
				err = p.metricsRegistry.UpdateCounter(
					context.Background(),
					metrics.MetricPTEHeadSamplingRejected,
					float64(rejected),
					map[string]string{"processor": p.registryID},
				)
				if err != nil {
					p.logger.Error("Failed to update rejected spans metric", zap.Error(err))
				}
			}

		case <-p.stopCh:
			return
		}
	}
}