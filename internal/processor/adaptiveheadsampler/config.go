package adaptiveheadsampler

import (
	"fmt"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/processor"
)

// Config defines configuration for the adaptive head sampler processor.
type Config struct {
	// InitialProbability is the starting probability for the head sampler
	InitialProbability float64 `mapstructure:"initial_probability"`
	
	// MinP is the minimum probability allowed for the head sampler
	MinP float64 `mapstructure:"min_p"`
	
	// MaxP is the maximum probability allowed for the head sampler
	MaxP float64 `mapstructure:"max_p"`
	
	// HashSeedConfig specifies the hash algorithm to use
	// Valid values are "XORTraceID" (for traces) and "RecordID" (for logs)
	HashSeedConfig string `mapstructure:"hash_seed_config"`
	
	// RecordIDFields specifies the fields to hash for logs (only used when HashSeedConfig is "RecordID")
	RecordIDFields []string `mapstructure:"record_id_fields"`
}

var _ component.Config = (*Config)(nil)

// Validate validates the processor configuration.
func (cfg *Config) Validate() error {
	if cfg.InitialProbability < 0 || cfg.InitialProbability > 1 {
		return fmt.Errorf("initial_probability must be between 0 and 1, got %f", cfg.InitialProbability)
	}
	
	if cfg.MinP < 0 || cfg.MinP > 1 {
		return fmt.Errorf("min_p must be between 0 and 1, got %f", cfg.MinP)
	}
	
	if cfg.MaxP < 0 || cfg.MaxP > 1 {
		return fmt.Errorf("max_p must be between 0 and 1, got %f", cfg.MaxP)
	}
	
	if cfg.MinP > cfg.MaxP {
		return fmt.Errorf("min_p (%f) cannot be greater than max_p (%f)", cfg.MinP, cfg.MaxP)
	}
	
	if cfg.HashSeedConfig != "XORTraceID" && cfg.HashSeedConfig != "RecordID" {
		return fmt.Errorf("hash_seed_config must be either 'XORTraceID' or 'RecordID', got %s", cfg.HashSeedConfig)
	}
	
	if cfg.HashSeedConfig == "RecordID" && len(cfg.RecordIDFields) == 0 {
		return fmt.Errorf("record_id_fields must be specified when hash_seed_config is 'RecordID'")
	}
	
	return nil
}

// CreateDefaultConfig creates the default configuration for the processor.
func createDefaultConfig() component.Config {
	return &Config{
		InitialProbability: 1.0, // By default, sample everything
		MinP:               0.01,
		MaxP:               1.0,
		HashSeedConfig:     "XORTraceID",
	}
}