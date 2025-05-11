package topnprocfilter

import (
	"context"
	"time"

	"github.com/deepaucksharma-nr/phoenix-core/internal/metrics"
	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"
)

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
	
	return nil
}

// reportMetrics periodically reports metrics
func (tp *topNProcessor) reportMetrics() {
	for {
		select {
		case <-tp.metricsTicker.C:
			ctx := context.Background()
			attrs := map[string]string{"processor": "topnprocfilter"}
			
			// Report top-N value
			err := tp.metricsRegistry.UpdateGauge(
				ctx,
				metrics.MetricPTEProcessTopN,
				float64(tp.n),
				attrs,
			)
			if err != nil {
				tp.logger.Error("Failed to update top-N metric", zap.Error(err))
			}
			
			// Report total processes
			var processCount int
			tp.processes.Range(func(_, _ interface{}) bool {
				processCount++
				return true
			})
			
			err = tp.metricsRegistry.UpdateGauge(
				ctx,
				metrics.MetricPTEProcessTotal,
				float64(processCount),
				attrs,
			)
			if err != nil {
				tp.logger.Error("Failed to update total processes metric", zap.Error(err))
			}
			
			// Report filtered processes count
			filtered := tp.filteredCount.Swap(0)
			if filtered > 0 {
				err = tp.metricsRegistry.UpdateCounter(
					ctx,
					metrics.MetricPTEProcessFiltered,
					float64(filtered),
					attrs,
				)
				if err != nil {
					tp.logger.Error("Failed to update filtered processes metric", zap.Error(err))
				}
			}
			
			// Report current thresholds
			err = tp.metricsRegistry.UpdateGauge(
				ctx,
				metrics.MetricPTEProcessThresholdCpu,
				tp.currentCPUThreshold.Load(),
				attrs,
			)
			if err != nil {
				tp.logger.Error("Failed to update CPU threshold metric", zap.Error(err))
			}
			
			err = tp.metricsRegistry.UpdateGauge(
				ctx,
				metrics.MetricPTEProcessThresholdMemory,
				tp.currentMemThreshold.Load(),
				attrs,
			)
			if err != nil {
				tp.logger.Error("Failed to update memory threshold metric", zap.Error(err))
			}
			
		case <-tp.stopCh:
			return
		}
	}
}

// Start implements the component.Component interface.
func (tp *topNProcessor) Start(ctx context.Context, host component.Host) error {
	// Start metrics reporting ticker
	tp.metricsTicker = time.NewTicker(10 * time.Second)
	go tp.reportMetrics()
	
	tp.logger.Info("Top-N process filter started with metrics reporting")
	return nil
}

// Shutdown implements the component.Component interface.
func (tp *topNProcessor) Shutdown(ctx context.Context) error {
	if tp.metricsTicker != nil {
		tp.metricsTicker.Stop()
	}
	
	close(tp.stopCh)
	return nil
}