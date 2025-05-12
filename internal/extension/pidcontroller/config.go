package pidcontroller

import (
	"fmt"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
)

// Config defines configuration for the PID controller extension.
type Config struct {
	// Interval specifies how often to run the control loop
	Interval string `mapstructure:"interval"`
	
	// TargetQueueUtilizationHigh is the high threshold for queue utilization
	TargetQueueUtilizationHigh float64 `mapstructure:"target_queue_utilization_high"`
	
	// TargetQueueUtilizationLow is the low threshold for queue utilization
	TargetQueueUtilizationLow float64 `mapstructure:"target_queue_utilization_low"`
	
	// AdjustmentFactorUp is the multiplier for probability when queue utilization is low
	AdjustmentFactorUp float64 `mapstructure:"adjustment_factor_up"`
	
	// AdjustmentFactorDown is the multiplier for probability when queue utilization is high
	AdjustmentFactorDown float64 `mapstructure:"adjustment_factor_down"`
	
	// EWMAAlpha is the exponential weighted moving average factor for smoothing
	EWMAAlpha float64 `mapstructure:"ewma_alpha"`
	
	// AggressiveDropFactor is the multiplier for probability during sustained high utilization
	AggressiveDropFactor float64 `mapstructure:"aggressive_drop_factor"`
	
	// AggressiveDropWindowCount is the number of consecutive high utilization intervals before aggressive drop
	AggressiveDropWindowCount int `mapstructure:"aggressive_drop_window_count"`
	
	// MetricsEndpoint is the endpoint to scrape for metrics (typically /metrics)
	MetricsEndpoint string `mapstructure:"metrics_endpoint"`
	
	// ExporterNames is the list of exporter names to monitor queue metrics for
	ExporterNames []string `mapstructure:"exporter_names"`
	
	// TunableRegistryID is the ID of the tunable component in the registry
	TunableRegistryID string `mapstructure:"tunable_registry_id"`

	// SamplerRegistryID is the ID of the sampler in the registry (deprecated, use TunableRegistryID instead)
	SamplerRegistryID string `mapstructure:"sampler_registry_id"`
}

var _ component.Config = (*Config)(nil)

// Validate validates the extension configuration.
func (cfg *Config) Validate() error {
	if cfg.Interval == "" {
		return fmt.Errorf("interval must be specified")
	}
	
	// Validate interval format
	_, err := time.ParseDuration(cfg.Interval)
	if err != nil {
		return fmt.Errorf("invalid interval format: %w", err)
	}
	
	if cfg.TargetQueueUtilizationHigh <= 0 || cfg.TargetQueueUtilizationHigh > 1 {
		return fmt.Errorf("target_queue_utilization_high must be between 0 and 1, got %f", cfg.TargetQueueUtilizationHigh)
	}
	
	if cfg.TargetQueueUtilizationLow <= 0 || cfg.TargetQueueUtilizationLow > 1 {
		return fmt.Errorf("target_queue_utilization_low must be between 0 and 1, got %f", cfg.TargetQueueUtilizationLow)
	}
	
	if cfg.TargetQueueUtilizationLow >= cfg.TargetQueueUtilizationHigh {
		return fmt.Errorf("target_queue_utilization_low (%f) must be less than target_queue_utilization_high (%f)", 
			cfg.TargetQueueUtilizationLow, cfg.TargetQueueUtilizationHigh)
	}
	
	if cfg.AdjustmentFactorUp <= 0 {
		return fmt.Errorf("adjustment_factor_up must be greater than 0, got %f", cfg.AdjustmentFactorUp)
	}
	
	if cfg.AdjustmentFactorDown <= 0 || cfg.AdjustmentFactorDown >= 1 {
		return fmt.Errorf("adjustment_factor_down must be between 0 and 1, got %f", cfg.AdjustmentFactorDown)
	}
	
	if cfg.EWMAAlpha <= 0 || cfg.EWMAAlpha > 1 {
		return fmt.Errorf("ewma_alpha must be between 0 and 1, got %f", cfg.EWMAAlpha)
	}
	
	if cfg.AggressiveDropFactor <= 0 || cfg.AggressiveDropFactor >= 1 {
		return fmt.Errorf("aggressive_drop_factor must be between 0 and 1, got %f", cfg.AggressiveDropFactor)
	}
	
	if cfg.AggressiveDropWindowCount <= 0 {
		return fmt.Errorf("aggressive_drop_window_count must be greater than 0, got %d", cfg.AggressiveDropWindowCount)
	}
	
	if cfg.MetricsEndpoint == "" {
		return fmt.Errorf("metrics_endpoint must be specified")
	}
	
	if len(cfg.ExporterNames) == 0 {
		return fmt.Errorf("exporter_names must include at least one exporter")
	}
	
	// Check if at least one registry ID is specified (prefer TunableRegistryID)
	if cfg.TunableRegistryID == "" && cfg.SamplerRegistryID == "" {
		return fmt.Errorf("either tunable_registry_id or sampler_registry_id must be specified")
	}
	
	return nil
}

// CreateDefaultConfig creates the default configuration for the extension.
func createDefaultConfig() component.Config {
	return &Config{
		Interval:                   "15s",
		TargetQueueUtilizationHigh: 0.8,
		TargetQueueUtilizationLow:  0.2,
		AdjustmentFactorUp:         1.25,
		AdjustmentFactorDown:       0.8,
		EWMAAlpha:                  0.3,
		AggressiveDropFactor:       0.5,
		AggressiveDropWindowCount:  3,
		MetricsEndpoint:            "http://localhost:8888/metrics",
		ExporterNames:              []string{"otlphttp/newrelic_default"},
		TunableRegistryID:          "adaptive_head_sampler",
		SamplerRegistryID:          "", // Deprecated
	}
}