package adaptiveheadsampler

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/deepaucksharma-nr/phoenix-core/internal/metrics"
	"github.com/deepaucksharma-nr/phoenix-core/internal/pkg/tunableregistry"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
	"go.uber.org/zap"
)

// Common base structure for adaptive sampling processors
type adaptiveSamplerBase struct {
	config        *Config
	logger        *zap.Logger
	currentP      atomic.Value // stores float64
	logThrottle   atomic.Int64 // Timestamp of last log to throttle logging
	hasher        func([]byte) uint64
	registryID    string

	// Metrics
	metricsRegistry *metrics.MetricsRegistry
	rejectedCount   atomic.Int64
	processingCount atomic.Int64
	metricsTicker   *time.Ticker
	stopCh          chan struct{}
}

// ID implements the Tunable interface
func (p *adaptiveSamplerBase) ID() string {
	return p.registryID
}

// SetValue implements the Tunable interface
func (p *adaptiveSamplerBase) SetValue(key string, value float64) {
	if key == "probability" {
		p.setProbability(value)
	}
	// Ignore other keys for now
}

// GetValue implements the Tunable interface
func (p *adaptiveSamplerBase) GetValue(key string) float64 {
	if key == "probability" {
		return p.currentP.Load()
	}
	// Return 0 for unknown keys
	return 0
}

// setProbability updates the sampling probability (internal implementation)
func (p *adaptiveSamplerBase) setProbability(newP float64) {
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
				zap.Float64("new_p", newP),
				zap.String("registry_id", p.registryID))
		}
	}
}

// initMetrics initializes the metrics for the processor
func (p *adaptiveSamplerBase) initMetrics() error {
	// Initialize sampling probability gauge - use both new and legacy metrics for compatibility
	_, err := p.metricsRegistry.GetOrCreateGauge(
		metrics.MetricPTETraceHeadSamplingProbability,
		metrics.DescPTETraceHeadSamplingProbability,
		metrics.UnitRatio,
	)
	if err != nil {
		return err
	}

	// Legacy metric
	_, err = p.metricsRegistry.GetOrCreateGauge(
		metrics.MetricPTEHeadSamplingProbability,
		metrics.DescPTEHeadSamplingProbability,
		metrics.UnitRatio,
	)
	if err != nil {
		return err
	}

	// Initialize throughput gauge
	_, err = p.metricsRegistry.GetOrCreateGauge(
		metrics.MetricPTETraceHeadSamplingThroughput,
		metrics.DescPTETraceHeadSamplingThroughput,
		metrics.UnitSpans,
	)
	if err != nil {
		return err
	}

	// Legacy metric
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
		metrics.MetricPTETraceHeadSamplingRejected,
		metrics.DescPTETraceHeadSamplingRejected,
		metrics.UnitSpans,
	)
	if err != nil {
		return err
	}

	// Legacy metric
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

// initLogMetrics initializes log-specific metrics for the processor
func (p *adaptiveHeadSamplerLogProcessor) initLogMetrics() error {
	// Initialize log-specific sampling metrics instead of base metrics

	// Initialize sampling probability gauge
	_, err := p.metricsRegistry.GetOrCreateGauge(
		metrics.MetricPTELogHeadSamplingProbability,
		metrics.DescPTELogHeadSamplingProbability,
		metrics.UnitRatio,
	)
	if err != nil {
		return err
	}

	// Initialize throughput gauge
	_, err = p.metricsRegistry.GetOrCreateGauge(
		metrics.MetricPTELogHeadSamplingThroughput,
		metrics.DescPTELogHeadSamplingThroughput,
		metrics.UnitSpans, // Reusing spans unit for consistency
	)
	if err != nil {
		return err
	}

	// Initialize rejected logs counter
	_, err = p.metricsRegistry.GetOrCreateCounter(
		metrics.MetricPTELogHeadSamplingRejected,
		metrics.DescPTELogHeadSamplingRejected,
		metrics.UnitSpans, // Reusing spans unit for consistency
	)
	if err != nil {
		return err
	}

	// Initialize content filter match counter
	_, err = p.metricsRegistry.GetOrCreateCounter(
		metrics.MetricPTELogSamplingContentFilterMatches,
		metrics.DescPTELogSamplingContentFilterMatches,
		metrics.UnitSpans, // Reusing spans unit for consistency
	)
	if err != nil {
		return err
	}

	// Initialize content filter include counter
	_, err = p.metricsRegistry.GetOrCreateCounter(
		metrics.MetricPTELogSamplingContentFilterIncludes,
		metrics.DescPTELogSamplingContentFilterIncludes,
		metrics.UnitSpans,
	)
	if err != nil {
		return err
	}

	// Initialize content filter exclude counter
	_, err = p.metricsRegistry.GetOrCreateCounter(
		metrics.MetricPTELogSamplingContentFilterExcludes,
		metrics.DescPTELogSamplingContentFilterExcludes,
		metrics.UnitSpans,
	)
	if err != nil {
		return err
	}

	// Initialize content filter weighted counter
	_, err = p.metricsRegistry.GetOrCreateCounter(
		metrics.MetricPTELogSamplingContentFilterWeighted,
		metrics.DescPTELogSamplingContentFilterWeighted,
		metrics.UnitSpans,
	)
	if err != nil {
		return err
	}

	// Initialize critical log match counter
	_, err = p.metricsRegistry.GetOrCreateCounter(
		metrics.MetricPTELogSamplingCriticalMatches,
		metrics.DescPTELogSamplingCriticalMatches,
		metrics.UnitSpans, // Reusing spans unit for consistency
	)
	if err != nil {
		return err
	}

	// Initialize critical log by level counter
	_, err = p.metricsRegistry.GetOrCreateCounter(
		metrics.MetricPTELogSamplingCriticalByLevel,
		metrics.DescPTELogSamplingCriticalByLevel,
		metrics.UnitSpans, // Reusing spans unit for consistency
	)
	if err != nil {
		return err
	}

	return nil
}

// reportMetrics periodically reports metrics for trace processor
func (p *adaptiveSamplerBase) reportMetrics() {
	for {
		select {
		case <-p.metricsTicker.C:
			currentP := p.currentP.Load()
			labels := map[string]string{"processor": p.registryID}

			// Report current probability - update both new and legacy metrics
			if err := p.metricsRegistry.UpdateGauge(
				context.Background(),
				metrics.MetricPTETraceHeadSamplingProbability,
				currentP,
				labels,
			); err != nil {
				p.logger.Error("Failed to update trace probability metric", zap.Error(err))
			}

			// Legacy metric
			if err := p.metricsRegistry.UpdateGauge(
				context.Background(),
				metrics.MetricPTEHeadSamplingProbability,
				currentP,
				labels,
			); err != nil {
				p.logger.Error("Failed to update legacy probability metric", zap.Error(err))
			}

			// Report throughput (spans per second)
			count := p.processingCount.Swap(0)
			throughput := float64(count) / 10.0 // 10 second interval

			if err := p.metricsRegistry.UpdateGauge(
				context.Background(),
				metrics.MetricPTETraceHeadSamplingThroughput,
				throughput,
				labels,
			); err != nil {
				p.logger.Error("Failed to update trace throughput metric", zap.Error(err))
			}

			// Legacy metric
			if err := p.metricsRegistry.UpdateGauge(
				context.Background(),
				metrics.MetricPTEHeadSamplingThroughput,
				throughput,
				labels,
			); err != nil {
				p.logger.Error("Failed to update legacy throughput metric", zap.Error(err))
			}

			// Report rejected spans
			rejected := p.rejectedCount.Swap(0)
			if rejected > 0 {
				if err := p.metricsRegistry.UpdateCounter(
					context.Background(),
					metrics.MetricPTETraceHeadSamplingRejected,
					float64(rejected),
					labels,
				); err != nil {
					p.logger.Error("Failed to update trace rejected spans metric", zap.Error(err))
				}

				// Legacy metric
				if err := p.metricsRegistry.UpdateCounter(
					context.Background(),
					metrics.MetricPTEHeadSamplingRejected,
					float64(rejected),
					labels,
				); err != nil {
					p.logger.Error("Failed to update legacy rejected spans metric", zap.Error(err))
				}
			}

		case <-p.stopCh:
			return
		}
	}
}

// reportLogMetrics periodically reports metrics specific to the log processor
func (p *adaptiveHeadSamplerLogProcessor) reportLogMetrics() {
	for {
		select {
		case <-p.metricsTicker.C:
			labels := map[string]string{"processor": p.registryID}

			// Report core log sampling metrics
			currentP := p.currentP.Load()
			if err := p.metricsRegistry.UpdateGauge(
				context.Background(),
				metrics.MetricPTELogHeadSamplingProbability,
				currentP,
				labels,
			); err != nil {
				p.logger.Error("Failed to update log probability metric", zap.Error(err))
			}

			// Report throughput (logs per second)
			count := p.processingCount.Swap(0)
			throughput := float64(count) / 10.0 // 10 second interval
			if err := p.metricsRegistry.UpdateGauge(
				context.Background(),
				metrics.MetricPTELogHeadSamplingThroughput,
				throughput,
				labels,
			); err != nil {
				p.logger.Error("Failed to update log throughput metric", zap.Error(err))
			}

			// Report rejected logs
			rejected := p.rejectedCount.Swap(0)
			if rejected > 0 {
				if err := p.metricsRegistry.UpdateCounter(
					context.Background(),
					metrics.MetricPTELogHeadSamplingRejected,
					float64(rejected),
					labels,
				); err != nil {
					p.logger.Error("Failed to update log rejected metric", zap.Error(err))
				}
			}

			// Report content filter matches
			matches := p.contentFilterMatches.Swap(0)
			if matches > 0 {
				if err := p.metricsRegistry.UpdateCounter(
					context.Background(),
					metrics.MetricPTELogSamplingContentFilterMatches,
					float64(matches),
					labels,
				); err != nil {
					p.logger.Error("Failed to update content filter matches metric", zap.Error(err))
				}
			}

			// Report content filter includes
			includes := p.contentFilterIncludes.Swap(0)
			if includes > 0 {
				if err := p.metricsRegistry.UpdateCounter(
					context.Background(),
					metrics.MetricPTELogSamplingContentFilterIncludes,
					float64(includes),
					labels,
				); err != nil {
					p.logger.Error("Failed to update content filter includes metric", zap.Error(err))
				}
			}

			// Report content filter excludes
			excludes := p.contentFilterExcludes.Swap(0)
			if excludes > 0 {
				if err := p.metricsRegistry.UpdateCounter(
					context.Background(),
					metrics.MetricPTELogSamplingContentFilterExcludes,
					float64(excludes),
					labels,
				); err != nil {
					p.logger.Error("Failed to update content filter excludes metric", zap.Error(err))
				}
			}

			// Report content filter weighted
			weighted := p.contentFilterWeighted.Swap(0)
			if weighted > 0 {
				if err := p.metricsRegistry.UpdateCounter(
					context.Background(),
					metrics.MetricPTELogSamplingContentFilterWeighted,
					float64(weighted),
					labels,
				); err != nil {
					p.logger.Error("Failed to update content filter weighted metric", zap.Error(err))
				}
			}

			// Report critical log matches
			criticalMatches := p.criticalLogMatches.Swap(0)
			if criticalMatches > 0 {
				if err := p.metricsRegistry.UpdateCounter(
					context.Background(),
					metrics.MetricPTELogSamplingCriticalMatches,
					float64(criticalMatches),
					labels,
				); err != nil {
					p.logger.Error("Failed to update critical log matches metric", zap.Error(err))
				}
			}

			// Report critical logs by severity level
			p.criticalLogLevels.Range(func(key, value interface{}) bool {
				level := key.(int)
				counter, ok := value.(*atomic.Int64)
				if !ok {
					return true // continue iteration
				}

				count := counter.Swap(0)
				if count > 0 {
					levelLabels := make(map[string]string)
					for k, v := range labels {
						levelLabels[k] = v
					}
					levelLabels["severity_level"] = fmt.Sprintf("%d", level)

					if err := p.metricsRegistry.UpdateCounter(
						context.Background(),
						metrics.MetricPTELogSamplingCriticalByLevel,
						float64(count),
						levelLabels,
					); err != nil {
						p.logger.Error("Failed to update critical log by level metric",
							zap.Error(err),
							zap.Int("level", level))
					}
				}

				return true // continue iteration
			})

		case <-p.stopCh:
			return
		}
	}
}

// xorTraceIDHasher creates a 64-bit hash from a trace ID by XORing its high and low parts
func (p *adaptiveSamplerBase) xorTraceIDHasher(id []byte) uint64 {
	if len(id) != 16 {
		// Should never happen with valid trace IDs
		return 0
	}
	
	// Extract high and low 64-bit parts and XOR them
	high := binary.BigEndian.Uint64(id[0:8])
	low := binary.BigEndian.Uint64(id[8:16])
	return high ^ low
}

// recordIDHasher hashes the string ID fields using xxHash
func (p *adaptiveSamplerBase) recordIDHasher(id []byte) uint64 {
	// For trace IDs, just use a simple hash
	if len(id) == 16 {
		return p.xorTraceIDHasher(id)
	}

	// For record IDs (strings), use xxHash (much faster than SHA1)
	return xxhash.Sum64(id)
}

// Start implements component.Component for the base
func (p *adaptiveSamplerBase) Start(ctx context.Context, host component.Host) error {
	// Start a background goroutine to report metrics periodically
	p.metricsTicker = time.NewTicker(10 * time.Second)
	go p.reportMetrics()
	
	p.logger.Info("Adaptive head sampler processor started with metrics reporting", 
		zap.String("registry_id", p.registryID))
	return nil
}

// Shutdown implements component.Component for the base
func (p *adaptiveSamplerBase) Shutdown(ctx context.Context) error {
	if p.metricsTicker != nil {
		p.metricsTicker.Stop()
	}
	
	close(p.stopCh)
	return nil
}

// Trace-specific adaptive head sampler implementation
type adaptiveHeadSamplerTraceProcessor struct {
	adaptiveSamplerBase
	nextConsumer  consumer.Traces
	traceIDRand   *rand.Rand
	traceIDRandMu sync.Mutex
}

// Ensure processors implement required interfaces
var _ processor.Traces = (*adaptiveHeadSamplerTraceProcessor)(nil)

// newTracesProcessor creates a new head sampling processor for traces.
func newTracesProcessor(settings processor.CreateSettings, config component.Config, nextConsumer consumer.Traces) (processor.Traces, error) {
	cfg := config.(*Config)

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Use TunableRegistryID from config if specified, otherwise use default
	registryID := "adaptive_head_sampler_traces" // default value
	if cfg.TunableRegistryID != "" {
		registryID = cfg.TunableRegistryID
	}

	base := adaptiveSamplerBase{
		config:          cfg,
		logger:          settings.Logger,
		registryID:      registryID,
		metricsRegistry: metrics.GetInstance(settings.Logger),
		stopCh:          make(chan struct{}),
	}
	
	// Set the initial probability
	base.currentP.Store(cfg.InitialProbability)
	
	// Configure the hasher based on the hash seed config
	if cfg.HashSeedConfig == "XORTraceID" {
		base.hasher = base.xorTraceIDHasher
	} else {
		base.hasher = base.recordIDHasher
	}
	
	// Register with the registry
	registry := tunableregistry.GetInstance()
	registry.Register(&base)
	
	// Initialize metrics
	if err := base.initMetrics(); err != nil {
		return nil, fmt.Errorf("failed to initialize metrics: %w", err)
	}

	p := &adaptiveHeadSamplerTraceProcessor{
		adaptiveSamplerBase: base,
		nextConsumer:        nextConsumer,
		traceIDRand:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
		
	p.logger.Info("Adaptive head sampler for traces created",
		zap.Float64("initial_probability", cfg.InitialProbability),
		zap.Float64("min_p", cfg.MinP),
		zap.Float64("max_p", cfg.MaxP),
		zap.String("hash_seed_config", cfg.HashSeedConfig),
		zap.String("registry_id", registryID))
	
	return p, nil
}

// ConsumeTraces implements the processor.Traces interface
func (p *adaptiveHeadSamplerTraceProcessor) ConsumeTraces(ctx context.Context, traces ptrace.Traces) error {
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
				rs := findOrCreateResource(sampledTraces, rs.Resource())
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
func (p *adaptiveHeadSamplerTraceProcessor) shouldSample(traceID pcommon.TraceID) bool {
	hash := p.adaptiveSamplerBase.hasher(traceID[:])
	
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

// Capabilities returns the capabilities of the processor.
func (p *adaptiveHeadSamplerTraceProcessor) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: true}
}

// GetProbability returns the current sampling probability.
func (p *adaptiveHeadSamplerTraceProcessor) GetProbability() float64 {
	return p.currentP.Load()
}

// SetProbability sets the sampling probability.
func (p *adaptiveHeadSamplerTraceProcessor) SetProbability(probability float64) {
	p.setProbability(probability)
}

// Start implements the component.Component interface.
func (p *adaptiveHeadSamplerTraceProcessor) Start(ctx context.Context, host component.Host) error {
	return p.adaptiveSamplerBase.Start(ctx, host)
}

// Shutdown implements the component.Component interface.
func (p *adaptiveHeadSamplerTraceProcessor) Shutdown(ctx context.Context) error {
	return p.adaptiveSamplerBase.Shutdown(ctx)
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

	// Get the parent ResourceSpans from the resource - we need to find it
	rs := findResourceSpansFromResource(targetResource)

	// Create a new scope in the resource spans
	ss := rs.ScopeSpans().AppendEmpty()
	sourceScope.Scope().CopyTo(ss.Scope())
	return ss
}

// Helper function to find ResourceSpans containing a Resource
func findResourceSpansFromResource(resource pcommon.Resource) ptrace.ResourceSpans {
	// This is just a placeholder - in reality, you'd need to actually find the ResourceSpans
	// that contains this resource, but there's no direct way to do this.
	// For this implementation, we'll assume we need to create a new ResourceSpans.

	// Create a new traces object to get a ResourceSpans
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()

	// Copy the resource to the new ResourceSpans
	resource.CopyTo(rs.Resource())

	return rs
}

// matchPattern checks if value matches a pattern with wildcard support
// Pattern can contain '*' as a wildcard to match any sequence of characters
func matchPattern(pattern, value string) bool {
	// Fast path: exact match
	if pattern == value {
		return true
	}
	
	// Fast path: '*' matches everything
	if pattern == "*" {
		return true
	}
	
	// If no wildcards, it's just an exact match check
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}
	
	// Convert the pattern to a regex pattern
	regexPattern := strings.ReplaceAll(pattern, "*", ".*")
	regexPattern = "^" + regexPattern + "$"
	
	// Compile the regex
	regex, err := regexp.Compile(regexPattern)
	if err != nil {
		// If the regex compilation fails, fall back to simple matching
		return pattern == value
	}
	
	// Return whether the value matches the pattern
	return regex.MatchString(value)
}

// getNestedValue retrieves a value from a nested map using dot notation
// e.g., "attributes.http.method" would access map["attributes"]["http"]["method"]
func getNestedValue(attributes pcommon.Map, fieldPath string) (string, bool) {
	parts := strings.Split(fieldPath, ".")
	
	// Handle top-level field
	if len(parts) == 1 {
		val, ok := attributes.Get(fieldPath)
		if !ok {
			return "", false
		}
		return val.AsString(), true
	}
	
	// Handle nested fields
	current := attributes
	for i, part := range parts {
		// Last part of the path
		if i == len(parts)-1 {
			val, ok := current.Get(part)
			if !ok {
				return "", false
			}
			return val.AsString(), true
		}
		
		// Get the next level map
		val, ok := current.Get(part)
		if !ok {
			return "", false
		}
		
		// Ensure the value is a map
		if val.Type() != pcommon.ValueTypeMap {
			return "", false
		}
		
		current = val.Map()
	}
	
	return "", false
}

// Log-specific adaptive head sampler implementation
type adaptiveHeadSamplerLogProcessor struct {
	adaptiveSamplerBase
	recordIDRand   *rand.Rand
	recordIDRandMu sync.Mutex

	// Metrics for content filtering
	contentFilterMatches    atomic.Int64 // Count of logs that matched a content filter
	contentFilterIncludes   atomic.Int64 // Count of logs forcibly included by content filters
	contentFilterExcludes   atomic.Int64 // Count of logs forcibly excluded by content filters
	contentFilterWeighted   atomic.Int64 // Count of logs that used weighted probability

	// Metrics for critical log handling
	criticalLogMatches      atomic.Int64 // Count of logs that matched a critical log pattern
	criticalLogLevels       sync.Map     // Map[int]atomic.Int64 - counts by severity level
}

// newLogsProcessor creates a new head sampling processor for logs.
func newLogsProcessor(settings processor.CreateSettings, cfg component.Config) (*adaptiveHeadSamplerLogProcessor, error) {
	config := cfg.(*Config)

	// Validate configuration
	if err := config.Validate(); err != nil {
		return nil, err
	}

	// Ensure RecordID hash mode is used for logs
	if config.HashSeedConfig != "RecordID" {
		settings.Logger.Warn("Changing HashSeedConfig from XORTraceID to RecordID for logs sampling",
			zap.String("original_config", config.HashSeedConfig))
		config.HashSeedConfig = "RecordID"
	}

	// Handle log field configuration
	// Case 1: No fields defined at all - use defaults
	if len(config.RecordIDFields) == 0 && len(config.RecordIDFieldsWithWeight) == 0 {
		// Default to some reasonable fields if none specified
		config.RecordIDFields = []string{"log.file.path", "k8s.pod.name", "message"}
		settings.Logger.Info("Using default RecordIDFields for logs sampling",
			zap.Strings("fields", config.RecordIDFields))
	}

	// Case 2: Simple fields defined but no weighted fields - convert simple to weighted
	if len(config.RecordIDFields) > 0 && len(config.RecordIDFieldsWithWeight) == 0 {
		// Convert simple fields to weighted fields with default weight 1.0
		for _, field := range config.RecordIDFields {
			config.RecordIDFieldsWithWeight = append(config.RecordIDFieldsWithWeight,
				&RecordIDField{
					Field:  field,
					Weight: 1.0,
				})
		}
		settings.Logger.Debug("Converted simple RecordIDFields to RecordIDFieldsWithWeight",
			zap.Int("field_count", len(config.RecordIDFieldsWithWeight)))
	}

	// Case 3: Both simple and weighted fields defined - log warning about weighted taking precedence
	if len(config.RecordIDFields) > 0 && len(config.RecordIDFieldsWithWeight) > 0 {
		settings.Logger.Warn("Both RecordIDFields and RecordIDFieldsWithWeight are defined, using RecordIDFieldsWithWeight",
			zap.Strings("simple_fields", config.RecordIDFields),
			zap.Int("weighted_fields_count", len(config.RecordIDFieldsWithWeight)))
	}

	// Set default weights for any weighted fields that don't have weights specified
	for i, field := range config.RecordIDFieldsWithWeight {
		if field.Weight == 0 {
			config.RecordIDFieldsWithWeight[i].Weight = 1.0
		}
	}

	// Use TunableRegistryID from config if specified, otherwise use default
	registryID := "adaptive_head_sampler_logs" // default value
	if config.TunableRegistryID != "" {
		registryID = config.TunableRegistryID
	}

	base := adaptiveSamplerBase{
		config:          config,
		logger:          settings.Logger,
		registryID:      registryID,
		metricsRegistry: metrics.GetInstance(settings.Logger),
		stopCh:          make(chan struct{}),
	}
	
	// Set the initial probability
	base.currentP.Store(config.InitialProbability)
	
	// Configure the hasher - for logs we always use recordIDHasher
	base.hasher = base.recordIDHasher
	
	// Register with the registry
	registry := tunableregistry.GetInstance()
	registry.Register(&base)
	
	p := &adaptiveHeadSamplerLogProcessor{
		adaptiveSamplerBase: base,
		recordIDRand:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}

	// Initialize both base and log-specific metrics
	if err := p.initLogMetrics(); err != nil {
		return nil, fmt.Errorf("failed to initialize metrics: %w", err)
	}
	
	p.logger.Info("Adaptive head sampler for logs created",
		zap.Float64("initial_probability", config.InitialProbability),
		zap.Float64("min_p", config.MinP),
		zap.Float64("max_p", config.MaxP),
		zap.Strings("record_id_fields", config.RecordIDFields),
		zap.String("registry_id", registryID))
	
	return p, nil
}

// Start implements component.Component for the log processor
func (p *adaptiveHeadSamplerLogProcessor) Start(ctx context.Context, host component.Host) error {
	// Start base metrics reporting
	err := p.adaptiveSamplerBase.Start(ctx, host)
	if err != nil {
		return err
	}
	
	// Start log-specific metrics reporting
	go p.reportLogMetrics()
	
	// Log detailed information about the record ID fields configuration
	recordIDFieldInfo := make([]string, 0, len(p.config.RecordIDFieldsWithWeight))
	for _, field := range p.config.RecordIDFieldsWithWeight {
		recordIDFieldInfo = append(recordIDFieldInfo,
			fmt.Sprintf("%s (weight:%.1f%s%s)",
				field.Field,
				field.Weight,
				func() string {
					if field.Required {
						return ", required"
					}
					return ""
				}(),
				func() string {
					if field.DefaultValue != "" {
						return fmt.Sprintf(", default:%s", field.DefaultValue)
					}
					return ""
				}()))
	}

	p.logger.Info("Adaptive head sampler log processor started with metrics reporting",
		zap.String("registry_id", p.registryID),
		zap.Int("content_filters", len(p.config.ContentFilters)),
		zap.Int("critical_log_patterns", len(p.config.CriticalLogPatterns)),
		zap.Strings("record_id_fields", recordIDFieldInfo))
	
	return nil
}

// Shutdown implements component.Component for the log processor
func (p *adaptiveHeadSamplerLogProcessor) Shutdown(ctx context.Context) error {
	// Base processor will close the stopCh which both goroutines listen to
	return p.adaptiveSamplerBase.Shutdown(ctx)
}

// processLogs is the main log sampling function
func (p *adaptiveHeadSamplerLogProcessor) processLogs(ctx context.Context, ld plog.Logs) (plog.Logs, error) {
	// Track counts for metrics
	totalLogs := countLogRecords(ld)
	p.processingCount.Add(int64(totalLogs))
	
	currentP := p.currentP.Load()
	if currentP >= 1.0 && len(p.config.ContentFilters) == 0 {
		// Fast path: if sampling at 100% and no content filtering, just pass through all logs
		return ld, nil
	}
	
	// Create a new Logs instance to store the sampled logs
	sampledLogs := plog.NewLogs()
	
	// Process each resource logs
	for i := 0; i < ld.ResourceLogs().Len(); i++ {
		rl := ld.ResourceLogs().At(i)
		resource := rl.Resource()
		
		// For each scope logs
		for j := 0; j < rl.ScopeLogs().Len(); j++ {
			sl := rl.ScopeLogs().At(j)
			
			// Process each log record
			for k := 0; k < sl.LogRecords().Len(); k++ {
				record := sl.LogRecords().At(k)
				
				var shouldSample bool
				
				// First check if this is a critical log that should always be sampled
				if p.checkCriticalLogPatterns(record, resource) {
					// If it's a critical log, always sample it
					shouldSample = true
				} else if len(p.config.ContentFilters) > 0 {
					// Then check content-based filters if any are configured
					filterAction, filterProbability := p.checkContentFilters(record, resource)

					switch filterAction {
					case ActionInclude:
						// Force include this record regardless of sampling probability
						p.contentFilterIncludes.Add(1)
						shouldSample = true
					case ActionExclude:
						// Force exclude this record regardless of sampling probability
						p.contentFilterExcludes.Add(1)
						shouldSample = false
					case ActionUseWeightedProbability:
						// Use the custom probability from the filter
						p.contentFilterWeighted.Add(1)
						// Create record ID for consistent hashing
						recordID := p.createRecordID(record, resource)
						// Check if it should be sampled with the custom probability
						shouldSample = p.shouldSampleLogWithProbability(recordID, filterProbability)
					default:
						// No matches found, use normal sampling logic
						recordID := p.createRecordID(record, resource)
						shouldSample = p.shouldSampleLog(recordID)
					}
				} else {
					// No content filters or critical patterns matched, use normal sampling logic
					recordID := p.createRecordID(record, resource)
					shouldSample = p.shouldSampleLog(recordID)
				}
				
				if !shouldSample {
					continue // Skip this record
				}
				
				// This record passed sampling, add it to sampledLogs
				// First, find or create the resource
				destRL := findOrCreateResourceLogs(sampledLogs, resource)
				// Then, find or create the scope
				destSL := findOrCreateScopeLogs(destRL, sl.Scope())
				// Finally, add the log record
				destRecord := destSL.LogRecords().AppendEmpty()
				record.CopyTo(destRecord)
			}
		}
	}
	
	// Count rejected logs for metrics
	sampledCount := countLogRecords(sampledLogs)
	rejectedCount := totalLogs - sampledCount
	if rejectedCount > 0 {
		p.rejectedCount.Add(int64(rejectedCount))
	}
	
	// If no logs were sampled, don't bother sending empty logs forward
	if sampledCount == 0 {
		return sampledLogs, nil
	}
	
	return sampledLogs, nil
}

// createRecordID creates a unique hash input for a log record based on configured fields
func (p *adaptiveHeadSamplerLogProcessor) createRecordID(record plog.LogRecord, resource pcommon.Resource) []byte {
	// The most common case is all fields have equal weight of 1.0,
	// which we can optimize by concatenating the values with a separator
	equalWeights := true
	firstWeight := float64(0)

	// Prepare to gather all values
	type fieldValueWithWeight struct {
		value  string
		weight float64
		field  string // for debugging
	}
	var fieldValues []fieldValueWithWeight
	var requiredMissing bool

	// Check if we have any required fields
	for i, fieldConfig := range p.config.RecordIDFieldsWithWeight {
		if i == 0 {
			firstWeight = fieldConfig.Weight
		} else if fieldConfig.Weight != firstWeight {
			equalWeights = false
		}

		// Get the field value
		var value string
		var exists bool

		// Special case for message body
		if fieldConfig.Field == "message" || fieldConfig.Field == "body" {
			value = record.Body().AsString()
			exists = true
		} else if strings.HasPrefix(fieldConfig.Field, "resource.") {
			// Resource attributes (remove the "resource." prefix)
			resourceField := strings.TrimPrefix(fieldConfig.Field, "resource.")
			value, exists = getNestedValue(resource.Attributes(), resourceField)
		} else {
			// Regular log record attributes
			value, exists = getNestedValue(record.Attributes(), fieldConfig.Field)
		}

		// If the field doesn't exist, check if required or use default
		if !exists || value == "" {
			if fieldConfig.Required {
				// A required field is missing - note this and abort
				p.logger.Debug("Required field missing for record ID",
					zap.String("field", fieldConfig.Field))
				requiredMissing = true
				break
			} else if fieldConfig.DefaultValue != "" {
				// Use the default value
				value = fieldConfig.DefaultValue
			} else {
				// Skip this field
				continue
			}
		}

		// Add the value to our collection
		fieldValues = append(fieldValues, fieldValueWithWeight{
			value:  value,
			weight: fieldConfig.Weight,
			field:  fieldConfig.Field,
		})
	}

	// If a required field is missing or no values were found, use timestamp + random value
	if requiredMissing || len(fieldValues) == 0 {
		fieldValues = []fieldValueWithWeight{
			{
				value:  fmt.Sprintf("%d", record.Timestamp()),
				weight: 1.0,
				field:  "timestamp",
			},
		}

		p.recordIDRandMu.Lock()
		randomVal := p.recordIDRand.Float64()
		p.recordIDRandMu.Unlock()

		fieldValues = append(fieldValues, fieldValueWithWeight{
			value:  fmt.Sprintf("%f", randomVal),
			weight: 1.0,
			field:  "random",
		})
	}

	// Simple case - all weights are equal
	if equalWeights {
		// Extract just the values
		values := make([]string, len(fieldValues))
		for i, fv := range fieldValues {
			values[i] = fv.value
		}

		// Sort values for consistent hashing
		sort.Strings(values)

		// Concatenate with a separator
		return []byte(strings.Join(values, ":"))
	}

	// Complex case - weights differ, need to sort by weight then field name
	sort.Slice(fieldValues, func(i, j int) bool {
		// First by weight (descending)
		if fieldValues[i].weight != fieldValues[j].weight {
			return fieldValues[i].weight > fieldValues[j].weight
		}
		// Then by field name (ascending)
		return fieldValues[i].field < fieldValues[j].field
	})

	// Build record ID with weights applied to the values
	var builder strings.Builder

	for i, fv := range fieldValues {
		// Apply weight by repeating the value proportionally
		// For example, with weight 2.0 we include the value twice
		repetitions := int(math.Ceil(fv.weight))
		for r := 0; r < repetitions; r++ {
			if builder.Len() > 0 {
				builder.WriteString(":")
			}
			builder.WriteString(fv.value)
		}
	}

	return []byte(builder.String())
}

// shouldSampleLog determines if a log record should be sampled using the current probability
func (p *adaptiveHeadSamplerLogProcessor) shouldSampleLog(recordID []byte) bool {
	return p.shouldSampleLogWithProbability(recordID, p.currentP.Load())
}

// shouldSampleLogWithProbability determines if a log record should be sampled using a specific probability
func (p *adaptiveHeadSamplerLogProcessor) shouldSampleLogWithProbability(recordID []byte, probability float64) bool {
	hash := p.adaptiveSamplerBase.hasher(recordID)
	
	// Convert hash to a value between 0 and 1
	randVal := float64(hash) / float64(^uint64(0))
	
	return randVal < probability
}

// checkCriticalLogPatterns checks if a log record matches any of the critical log patterns
// Returns true if the log should be treated as critical and sampled regardless of probability
func (p *adaptiveHeadSamplerLogProcessor) checkCriticalLogPatterns(record plog.LogRecord, resource pcommon.Resource) bool {
	if len(p.config.CriticalLogPatterns) == 0 {
		return false
	}

	// Check each critical log pattern in order
	for _, pattern := range p.config.CriticalLogPatterns {
		// Get the field value based on the pattern's field path
		var fieldValue string
		var exists bool

		// Special case for message body
		if pattern.Field == "message" || pattern.Field == "body" {
			fieldValue = record.Body().AsString()
			exists = true
		} else if strings.HasPrefix(pattern.Field, "resource.") {
			// Resource attributes (remove the "resource." prefix)
			resourceField := strings.TrimPrefix(pattern.Field, "resource.")
			fieldValue, exists = getNestedValue(resource.Attributes(), resourceField)
		} else {
			// Regular log record attributes
			fieldValue, exists = getNestedValue(record.Attributes(), pattern.Field)
		}

		// If the field doesn't exist, skip this pattern
		if !exists {
			continue
		}

		// Check if the value matches the pattern
		if matchPattern(pattern.Pattern, fieldValue) {
			// Count the match
			p.criticalLogMatches.Add(1)

			// Count by severity level
			levelCounter, _ := p.criticalLogLevels.LoadOrStore(
				pattern.SeverityLevel,
				&atomic.Int64{},
			)
			levelCounter.(*atomic.Int64).Add(1)

			// Log at debug level when a critical log pattern matches
			p.logger.Debug("Critical log pattern matched",
				zap.String("field", pattern.Field),
				zap.String("pattern", pattern.Pattern),
				zap.Int("severity_level", pattern.SeverityLevel),
				zap.String("value", fieldValue),
				zap.String("description", pattern.Description))

			// Critical log match means we should include the log
			return true
		}
	}

	// No critical patterns matched
	return false
}

// checkContentFilters checks if a log record matches any of the content filters
// Returns the action to take and any custom probability for weighted sampling
func (p *adaptiveHeadSamplerLogProcessor) checkContentFilters(record plog.LogRecord, resource pcommon.Resource) (FilterAction, float64) {
	if len(p.config.ContentFilters) == 0 {
		return "", 0.0
	}

	// Check each content filter in order
	for _, filter := range p.config.ContentFilters {
		// Get the field value based on the filter's field path
		var fieldValue string
		var exists bool

		// Special case for message body
		if filter.Field == "message" || filter.Field == "body" {
			fieldValue = record.Body().AsString()
			exists = true
		} else if strings.HasPrefix(filter.Field, "resource.") {
			// Resource attributes (remove the "resource." prefix)
			resourceField := strings.TrimPrefix(filter.Field, "resource.")
			fieldValue, exists = getNestedValue(resource.Attributes(), resourceField)
		} else {
			// Regular log record attributes
			fieldValue, exists = getNestedValue(record.Attributes(), filter.Field)
		}

		// If the field doesn't exist, skip this filter
		if !exists {
			continue
		}

		// Check if the value matches the pattern
		if matchPattern(filter.Pattern, fieldValue) {
			// Count the match
			p.contentFilterMatches.Add(1)

			// Log at debug level when a filter matches
			p.logger.Debug("Content filter matched",
				zap.String("field", filter.Field),
				zap.String("pattern", filter.Pattern),
				zap.String("action", string(filter.Action)),
				zap.String("value", fieldValue),
				zap.String("description", filter.Description))

			// Return the action and probability
			return filter.Action, filter.Probability
		}
	}

	// No filters matched
	return "", 0.0
}

// countLogRecords counts the total number of log records in a Logs object
func countLogRecords(logs plog.Logs) int {
	count := 0
	for i := 0; i < logs.ResourceLogs().Len(); i++ {
		rl := logs.ResourceLogs().At(i)
		for j := 0; j < rl.ScopeLogs().Len(); j++ {
			sl := rl.ScopeLogs().At(j)
			count += sl.LogRecords().Len()
		}
	}
	return count
}

// findOrCreateResourceLogs finds a matching resource in the target logs or creates a new one if it doesn't exist
func findOrCreateResourceLogs(target plog.Logs, source pcommon.Resource) plog.ResourceLogs {
	// For simplicity, always create a new resource
	// A more optimized version could try to find a matching resource
	rl := target.ResourceLogs().AppendEmpty()
	source.CopyTo(rl.Resource())
	return rl
}

// findOrCreateScopeLogs finds a matching scope in the target resource logs or creates a new one if it doesn't exist
func findOrCreateScopeLogs(targetRL plog.ResourceLogs, sourceScope pcommon.InstrumentationScope) plog.ScopeLogs {
	// For simplicity, always create a new scope
	// A more optimized version could try to find a matching scope
	sl := targetRL.ScopeLogs().AppendEmpty()
	sourceScope.CopyTo(sl.Scope())
	return sl
}