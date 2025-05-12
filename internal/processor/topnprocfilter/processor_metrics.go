package topnprocfilter

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deepaucksharma-nr/phoenix-core/internal/metrics"
	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"
)

// Constants for metrics reporting optimization
const (
	defaultMetricsReportingInterval = 10 * time.Second
	defaultMetricsBufferSize        = 100
	defaultMetricsBatchSize         = 10
	circuitBreakerThreshold         = 5   // Number of consecutive errors before opening circuit
	circuitBreakerResetInterval     = 30  // Seconds before trying to close circuit again
)

// metricUpdate represents a metric update operation to be processed
type metricUpdate struct {
	metricName string
	value      float64
	attrs      map[string]string
	metricType string // "gauge" or "counter"
}

// cleanupIdleProcesses starts the idle process cleanup
func (tp *topNProcessor) cleanupIdleProcesses(idleTTL time.Duration) {
	tp.periodicCleanup(idleTTL)
}

// initMetrics initializes the metrics for the processor
func (tp *topNProcessor) initMetrics() error {
	// Initialize top-N value gauge
	_, err := tp.metricsRegistry.GetOrCreateGauge(
		metrics.MetricPTEProcessTopN,
		metrics.DescPTEProcessTopN,
		metrics.UnitProcesses,
	)
	if err != nil {
		return err
	}
	
	// Initialize total processes gauge
	_, err = tp.metricsRegistry.GetOrCreateGauge(
		metrics.MetricPTEProcessTotal,
		metrics.DescPTEProcessTotal,
		metrics.UnitProcesses,
	)
	if err != nil {
		return err
	}
	
	// Initialize filtered processes counter
	_, err = tp.metricsRegistry.GetOrCreateCounter(
		metrics.MetricPTEProcessFiltered,
		metrics.DescPTEProcessFiltered,
		metrics.UnitProcesses,
	)
	if err != nil {
		return err
	}
	
	// Initialize CPU threshold gauge
	_, err = tp.metricsRegistry.GetOrCreateGauge(
		metrics.MetricPTEProcessThresholdCpu,
		metrics.DescPTEProcessThresholdCpu,
		metrics.UnitRatio,
	)
	if err != nil {
		return err
	}
	
	// Initialize memory threshold gauge
	_, err = tp.metricsRegistry.GetOrCreateGauge(
		metrics.MetricPTEProcessThresholdMemory,
		metrics.DescPTEProcessThresholdMemory,
		metrics.UnitBytes,
	)
	if err != nil {
		return err
	}

	// Initialize cache hit ratio gauge
	_, err = tp.metricsRegistry.GetOrCreateGauge(
		"pte_topn_cache_hit_ratio",
		"The ratio of cache hits to total cache operations in the top-N processor",
		metrics.UnitRatio,
	)
	if err != nil {
		return err
	}

	return nil
}

// reportMetrics periodically reports metrics using the optimized buffered approach
func (tp *topNProcessor) reportMetrics() {
	for {
		select {
		case <-tp.metricsTicker.C:
			attrs := map[string]string{"processor": "topnprocfilter"}

			// Report top-N value
			tp.submitMetricUpdate(
				metrics.MetricPTEProcessTopN,
				float64(tp.n),
				attrs,
				"gauge",
			)

			// Report total processes
			var processCount int
			tp.processes.Range(func(_, _ interface{}) bool {
				processCount++
				return true
			})

			tp.submitMetricUpdate(
				metrics.MetricPTEProcessTotal,
				float64(processCount),
				attrs,
				"gauge",
			)

			// Report filtered processes count
			filtered := tp.filteredCount.Swap(0)
			if filtered > 0 {
				tp.submitMetricUpdate(
					metrics.MetricPTEProcessFiltered,
					float64(filtered),
					attrs,
					"counter",
				)
			}

			// Report current thresholds
			tp.submitMetricUpdate(
				metrics.MetricPTEProcessThresholdCpu,
				tp.currentCPUThreshold.Load(),
				attrs,
				"gauge",
			)

			tp.submitMetricUpdate(
				metrics.MetricPTEProcessThresholdMemory,
				tp.currentMemThreshold.Load(),
				attrs,
				"gauge",
			)

			// Report cache efficiency metrics
			cacheHits := tp.cacheHitCount.Swap(0)
			cacheMisses := tp.cacheMissCount.Swap(0)

			// Calculate cache hit ratio if we have operations
			if cacheHits+cacheMisses > 0 {
				cacheRatio := float64(cacheHits) / float64(cacheHits+cacheMisses)

				// Report cache hit ratio
				tp.submitMetricUpdate(
					"pte_topn_cache_hit_ratio",
					cacheRatio,
					attrs,
					"gauge",
				)

				tp.logger.Debug("Cache efficiency metrics",
					zap.Int64("hits", cacheHits),
					zap.Int64("misses", cacheMisses),
					zap.Float64("hit_ratio", cacheRatio))
			}

			// Report metrics system health
			tp.submitMetricUpdate(
				"pte_topn_metrics_circuit_open",
				btof(tp.metricsCircuitOpen.Load()),
				attrs,
				"gauge",
			)

			tp.submitMetricUpdate(
				"pte_topn_metrics_error_count",
				float64(tp.metricsErrorCount.Load()),
				attrs,
				"gauge",
			)

		case <-tp.stopCh:
			return
		}
	}
}

// btof converts a boolean to float64 (1.0 for true, 0.0 for false)
func btof(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}

// initMetricsOptimizations initializes the metrics reporting optimizations
func (tp *topNProcessor) initMetricsOptimizations() {
	// Initialize metrics buffer and worker pool
	tp.metricsBuffer = make(chan metricUpdate, defaultMetricsBufferSize)
	tp.metricsBatchSize = defaultMetricsBatchSize
	tp.metricsReportingInterval = defaultMetricsReportingInterval

	// Start metrics processing workers
	tp.metricsWg.Add(1)
	go tp.processMetricsUpdates()
}

// processMetricsUpdates processes metrics updates in batches
func (tp *topNProcessor) processMetricsUpdates() {
	defer tp.metricsWg.Done()

	batch := make([]metricUpdate, 0, tp.metricsBatchSize)
	ticker := time.NewTicker(100 * time.Millisecond) // Process batches every 100ms

	for {
		select {
		case update, ok := <-tp.metricsBuffer:
			if !ok {
				// Channel closed, process remaining updates and exit
				if len(batch) > 0 {
					tp.processBatch(batch)
				}
				return
			}

			batch = append(batch, update)
			if len(batch) >= tp.metricsBatchSize {
				tp.processBatch(batch)
				batch = batch[:0] // Clear batch but keep capacity
			}

		case <-ticker.C:
			// Process any pending metrics in the batch
			if len(batch) > 0 {
				tp.processBatch(batch)
				batch = batch[:0] // Clear batch but keep capacity
			}

			// Check if circuit breaker should be reset
			if tp.metricsCircuitOpen.Load() {
				resetTime := time.Unix(tp.metricsCircuitResetTime.Load(), 0)
				if time.Now().After(resetTime) {
					tp.metricsCircuitOpen.Store(false)
					tp.metricsErrorCount.Store(0)
					tp.logger.Info("Metrics circuit breaker reset, resuming metrics reporting")
				}
			}

		case <-tp.stopCh:
			ticker.Stop()
			if len(batch) > 0 {
				tp.processBatch(batch)
			}
			return
		}
	}
}

// processBatch processes a batch of metric updates
func (tp *topNProcessor) processBatch(batch []metricUpdate) {
	if tp.metricsCircuitOpen.Load() {
		// Circuit is open, skip metrics reporting
		return
	}

	ctx := context.Background()
	errorCount := 0

	for _, update := range batch {
		var err error

		if update.metricType == "gauge" {
			err = tp.metricsRegistry.UpdateGauge(ctx, update.metricName, update.value, update.attrs)
		} else if update.metricType == "counter" {
			err = tp.metricsRegistry.UpdateCounter(ctx, update.metricName, update.value, update.attrs)
		}

		if err != nil {
			errorCount++
			tp.logger.Debug("Failed to update metric",
				zap.String("name", update.metricName),
				zap.Error(err))
		}
	}

	// Check if we need to open the circuit breaker
	if errorCount > 0 {
		currentErrors := tp.metricsErrorCount.Add(int64(errorCount))
		if currentErrors >= circuitBreakerThreshold {
			tp.metricsCircuitOpen.Store(true)
			tp.metricsCircuitResetTime.Store(time.Now().Add(time.Second * circuitBreakerResetInterval).Unix())
			tp.logger.Warn("Metrics circuit breaker opened due to repeated errors",
				zap.Int64("error_count", currentErrors),
				zap.Int("threshold", circuitBreakerThreshold),
				zap.Int("reset_seconds", circuitBreakerResetInterval))
		}
	} else {
		// Reset error count on successful batch
		tp.metricsErrorCount.Store(0)
	}
}

// submitMetricUpdate safely submits a metric update to the buffer
func (tp *topNProcessor) submitMetricUpdate(name string, value float64, attrs map[string]string, metricType string) {
	if tp.metricsCircuitOpen.Load() {
		return // Skip if circuit breaker is open
	}

	// Use non-blocking send to avoid getting stuck if buffer is full
	select {
	case tp.metricsBuffer <- metricUpdate{
		metricName: name,
		value:      value,
		attrs:      attrs,
		metricType: metricType,
	}:
		// Update sent successfully
	default:
		// Buffer full, log and discard
		tp.logger.Debug("Metrics buffer full, discarding update", zap.String("metric", name))
	}
}

// Start implements the component.Component interface.
func (tp *topNProcessor) Start(ctx context.Context, host component.Host) error {
	// Initialize metrics optimizations
	tp.initMetricsOptimizations()

	// Start metrics reporting ticker
	tp.metricsTicker = time.NewTicker(tp.metricsReportingInterval)
	go tp.reportMetrics()

	tp.logger.Info("Top-N process filter started with metrics reporting",
		zap.Duration("reporting_interval", tp.metricsReportingInterval),
		zap.Int("batch_size", tp.metricsBatchSize),
		zap.Int("buffer_size", defaultMetricsBufferSize))

	return nil
}

// Shutdown implements the component.Component interface.
func (tp *topNProcessor) Shutdown(ctx context.Context) error {
	if tp.metricsTicker != nil {
		tp.metricsTicker.Stop()
	}

	// Signal goroutines to stop
	close(tp.stopCh)

	// Close metrics buffer
	close(tp.metricsBuffer)

	// Wait for metrics processing to complete
	tp.metricsWg.Wait()

	return nil
}