package fallbackprocparser

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processorhelper"
)

const (
	// The value of "type" key in configuration.
	typeStr = "fallback_proc_parser"
	
	stability = component.StabilityLevelBeta
)

// NewFactory creates a factory for the fallback process parser processor.
func NewFactory() processor.Factory {
	return processor.NewFactory(
		typeStr,
		createDefaultConfig,
		processor.WithMetrics(createMetricsProcessor, stability),
	)
}

// createMetricsProcessor creates a metrics processor based on this config.
func createMetricsProcessor(
	ctx context.Context,
	set processor.CreateSettings,
	cfg component.Config,
	nextConsumer consumer.Metrics,
) (processor.Metrics, error) {
	processorConfig := cfg.(*Config)
	
	metricsProcessor, err := newProcessor(set, processorConfig)
	if err != nil {
		return nil, err
	}
	
	return processorhelper.NewMetricsProcessor(
		ctx,
		set,
		cfg,
		nextConsumer,
		metricsProcessor.processMetrics,
	)
}