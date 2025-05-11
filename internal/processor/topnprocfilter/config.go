package topnprocfilter

import (
	"fmt"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/processor"
)

// Config defines configuration for the topn process metrics filter processor.
type Config struct {
	// TopN is the number of top processes to retain per dimension (CPU, memory, etc)
	TopN int `mapstructure:"top_n"`
	
	// CPUThreshold is the minimum CPU usage threshold for inclusion (0.0-1.0)
	CPUThreshold float64 `mapstructure:"cpu_threshold"`
	
	// MemoryThreshold is the minimum memory usage threshold for inclusion (0.0-1.0)
	MemoryThreshold float64 `mapstructure:"memory_threshold"`
	
	// IdleTTL is how long to keep a process in the active set after it goes below thresholds
	// Format is a duration string (e.g., "5m" for 5 minutes)
	IdleTTL string `mapstructure:"idle_ttl"`
	
	// RegistryID is the ID to use when registering as a tunable with the registry
	RegistryID string `mapstructure:"registry_id"`
}

var _ component.Config = (*Config)(nil)

// Validate validates the processor configuration.
func (cfg *Config) Validate() error {
	if cfg.TopN <= 0 {
		return fmt.Errorf("top_n must be greater than 0, got %d", cfg.TopN)
	}
	
	if cfg.CPUThreshold < 0 || cfg.CPUThreshold > 1 {
		return fmt.Errorf("cpu_threshold must be between 0 and 1, got %f", cfg.CPUThreshold)
	}
	
	if cfg.MemoryThreshold < 0 || cfg.MemoryThreshold > 1 {
		return fmt.Errorf("memory_threshold must be between 0 and 1, got %f", cfg.MemoryThreshold)
	}
	
	if cfg.IdleTTL != "" {
		_, err := time.ParseDuration(cfg.IdleTTL)
		if err != nil {
			return fmt.Errorf("invalid idle_ttl format: %w", err)
		}
	}
	
	return nil
}

// CreateDefaultConfig creates the default configuration for the processor.
func createDefaultConfig() component.Config {
	return &Config{
		TopN:            50,
		CPUThreshold:    0.01, // 1% CPU usage
		MemoryThreshold: 0.01, // 1% memory usage
		IdleTTL:         "5m", // 5 minutes
		RegistryID:      "proc_top_n",
	}
}