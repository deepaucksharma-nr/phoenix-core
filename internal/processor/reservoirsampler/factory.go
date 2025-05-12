package reservoirsampler

import (
	"context"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/processor"
	"go.uber.org/zap"
)

const (
	// The value of "type" key in configuration.
	typeStr = "reservoir_sampler"
)

// NewFactory returns a new factory for the Reservoir Sampler processor.
func NewFactory() processor.Factory {
	return processor.NewFactory(
		typeStr,
		createDefaultConfig,
		processor.WithTraces(createTracesProcessor, component.StabilityLevelBeta),
	)
}

// createTracesProcessor creates a trace processor based on this config.
func createTracesProcessor(
	ctx context.Context,
	set processor.CreateSettings,
	cfg component.Config,
	nextConsumer consumer.Traces,
) (processor.Traces, error) {
	rsCfg := cfg.(*Config)

	// Log trace-aware configuration
	if rsCfg.TraceAware {
		traceTimeout := defaultTraceTimeoutDuration
		if rsCfg.TraceTimeout != "" {
			var err error
			traceTimeout, err = time.ParseDuration(rsCfg.TraceTimeout)
			if err != nil {
				// This shouldn't happen as the config is validated, but log a warning just in case
				set.Logger.Warn("Invalid trace timeout, using default",
					zap.String("configured_value", rsCfg.TraceTimeout),
					zap.Duration("default_value", defaultTraceTimeoutDuration),
					zap.Error(err))
			}
		}

		set.Logger.Info("Trace-aware reservoir sampling enabled",
			zap.Bool("trace_aware", rsCfg.TraceAware),
			zap.Duration("trace_timeout", traceTimeout))
	}

	return newProcessor(set, cfg, nextConsumer)
}
