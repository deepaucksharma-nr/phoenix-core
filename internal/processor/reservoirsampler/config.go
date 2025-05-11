package reservoirsampler

import (
	"fmt"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/processor"
)

// Config defines configuration for the reservoir sampler processor.
type Config struct {
	// SizeK is the maximum number of items to keep in the reservoir
	SizeK int `mapstructure:"size_k"`
	
	// WindowDuration is the time window for the reservoir in seconds
	WindowDuration string `mapstructure:"window_duration"`
	
	// CheckpointPath is the file path where reservoir state will be checkpointed
	CheckpointPath string `mapstructure:"checkpoint_path"`
	
	// CheckpointInterval is the interval between checkpoints in seconds
	CheckpointInterval string `mapstructure:"checkpoint_interval"`
}

var _ component.Config = (*Config)(nil)

// Validate validates the processor configuration.
func (cfg *Config) Validate() error {
	if cfg.SizeK <= 0 {
		return fmt.Errorf("size_k must be greater than 0, got %d", cfg.SizeK)
	}
	
	if cfg.WindowDuration == "" {
		return fmt.Errorf("window_duration must be specified")
	}
	
	// Validate window duration format
	_, err := time.ParseDuration(cfg.WindowDuration)
	if err != nil {
		return fmt.Errorf("invalid window_duration format: %w", err)
	}
	
	if cfg.CheckpointPath == "" {
		return fmt.Errorf("checkpoint_path must be specified")
	}
	
	if cfg.CheckpointInterval == "" {
		return fmt.Errorf("checkpoint_interval must be specified")
	}
	
	// Validate checkpoint interval format
	_, err = time.ParseDuration(cfg.CheckpointInterval)
	if err != nil {
		return fmt.Errorf("invalid checkpoint_interval format: %w", err)
	}
	
	return nil
}

// CreateDefaultConfig creates the default configuration for the processor.
func createDefaultConfig() component.Config {
	return &Config{
		SizeK:              5000,
		WindowDuration:     "60s",
		CheckpointPath:     "/data/pte/reservoir_state.db",
		CheckpointInterval: "10s",
	}
}