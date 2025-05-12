package pidcontroller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfigValidation(t *testing.T) {
	// Create a valid base config
	validConfig := &Config{
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
	}

	// Test valid config with TunableRegistryID
	err := validConfig.Validate()
	assert.NoError(t, err)

	// Test valid config with SamplerRegistryID (deprecated but still supported)
	oldStyleConfig := &Config{
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
		SamplerRegistryID:          "adaptive_head_sampler",
	}
	err = oldStyleConfig.Validate()
	assert.NoError(t, err)

	// Test invalid config with neither registry ID
	invalidConfig := &Config{
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
	}
	err = invalidConfig.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "either tunable_registry_id or sampler_registry_id must be specified")
}