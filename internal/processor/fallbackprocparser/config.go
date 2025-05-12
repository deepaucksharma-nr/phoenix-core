package fallbackprocparser

import (
	"fmt"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/processor"
)

// Config defines configuration for the fallback process parser processor.
type Config struct {
	// Enabled determines whether the fallback parser is active
	Enabled bool `mapstructure:"enabled"`

	// AttributesToFetch defines which attributes to attempt to fetch from /proc
	// Valid values are "command_line", "owner", "io", "executable_path"
	AttributesToFetch []string `mapstructure:"attributes_to_fetch"`

	// StrictMode determines whether to abort (drop metrics) or just warn
	// when critical attributes are missing
	StrictMode bool `mapstructure:"strict_mode"`

	// CriticalAttributes defines which attributes are considered critical
	// If any of these are missing after fallback attempts, behavior depends on StrictMode
	CriticalAttributes []string `mapstructure:"critical_attributes"`

	// CacheTTLSeconds defines how long to cache lookup results in seconds
	// Default is 300 seconds (5 minutes)
	CacheTTLSeconds int `mapstructure:"cache_ttl_seconds"`

	// CommandLineCacheTTLSeconds defines how long to cache command line results
	// Default is 300 seconds (5 minutes) if not specified (will use CacheTTLSeconds)
	CommandLineCacheTTLSeconds int `mapstructure:"command_line_cache_ttl_seconds"`

	// UsernameCacheTTLSeconds defines how long to cache username results
	// Default is 1800 seconds (30 minutes) if not specified (will use CacheTTLSeconds)
	UsernameCacheTTLSeconds int `mapstructure:"username_cache_ttl_seconds"`

	// ExecutablePathCacheTTLSeconds defines how long to cache executable path results
	// Default is 3600 seconds (1 hour) if not specified (will use CacheTTLSeconds)
	ExecutablePathCacheTTLSeconds int `mapstructure:"executable_path_cache_ttl_seconds"`

	// CircuitBreakerEnabled determines whether to use circuit breakers for slow operations
	// Default is true
	CircuitBreakerEnabled bool `mapstructure:"circuit_breaker_enabled"`

	// CircuitBreakerThresholdMs is the maximum allowed operation time in milliseconds
	// Operations exceeding this threshold will trigger the circuit breaker
	// Default is 100ms
	CircuitBreakerThresholdMs int `mapstructure:"circuit_breaker_threshold_ms"`

	// CircuitBreakerCooldownSeconds is the time in seconds to wait before trying a failed operation again
	// Default is 60 seconds
	CircuitBreakerCooldownSeconds int `mapstructure:"circuit_breaker_cooldown_seconds"`
}

var _ component.Config = (*Config)(nil)

// Validate validates the processor configuration.
func (cfg *Config) Validate() error {
	if !cfg.Enabled {
		return nil
	}

	for _, attr := range cfg.AttributesToFetch {
		if attr != "command_line" && attr != "owner" && attr != "io" && attr != "executable_path" {
			return fmt.Errorf("invalid attribute to fetch: %s. Valid values are 'command_line', 'owner', 'io', 'executable_path'", attr)
		}
	}

	if len(cfg.CriticalAttributes) == 0 {
		// Default critical attributes if none specified
		cfg.CriticalAttributes = []string{"process.executable.name", "process.command_line"}
	}

	// Validate cache TTL
	if cfg.CacheTTLSeconds < 0 {
		return fmt.Errorf("cache_ttl_seconds must be >= 0, got %d", cfg.CacheTTLSeconds)
	}

	// If cache TTL is 0, set it to default value (5 minutes)
	if cfg.CacheTTLSeconds == 0 {
		cfg.CacheTTLSeconds = 300
	}

	// Set default values for specific cache TTLs if not specified
	// Command line cache TTL (default: 5 minutes)
	if cfg.CommandLineCacheTTLSeconds < 0 {
		return fmt.Errorf("command_line_cache_ttl_seconds must be >= 0, got %d", cfg.CommandLineCacheTTLSeconds)
	}
	if cfg.CommandLineCacheTTLSeconds == 0 {
		cfg.CommandLineCacheTTLSeconds = cfg.CacheTTLSeconds
	}

	// Username cache TTL (default: 30 minutes)
	if cfg.UsernameCacheTTLSeconds < 0 {
		return fmt.Errorf("username_cache_ttl_seconds must be >= 0, got %d", cfg.UsernameCacheTTLSeconds)
	}
	if cfg.UsernameCacheTTLSeconds == 0 {
		cfg.UsernameCacheTTLSeconds = 1800 // 30 minutes
	}

	// Executable path cache TTL (default: 1 hour)
	if cfg.ExecutablePathCacheTTLSeconds < 0 {
		return fmt.Errorf("executable_path_cache_ttl_seconds must be >= 0, got %d", cfg.ExecutablePathCacheTTLSeconds)
	}
	if cfg.ExecutablePathCacheTTLSeconds == 0 {
		cfg.ExecutablePathCacheTTLSeconds = 3600 // 1 hour
	}

	// Validate circuit breaker settings
	if cfg.CircuitBreakerThresholdMs < 0 {
		return fmt.Errorf("circuit_breaker_threshold_ms must be >= 0, got %d", cfg.CircuitBreakerThresholdMs)
	}
	if cfg.CircuitBreakerThresholdMs == 0 {
		cfg.CircuitBreakerThresholdMs = 100 // 100ms default
	}

	if cfg.CircuitBreakerCooldownSeconds < 0 {
		return fmt.Errorf("circuit_breaker_cooldown_seconds must be >= 0, got %d", cfg.CircuitBreakerCooldownSeconds)
	}
	if cfg.CircuitBreakerCooldownSeconds == 0 {
		cfg.CircuitBreakerCooldownSeconds = 60 // 60 seconds default
	}

	return nil
}

// CreateDefaultConfig creates the default configuration for the processor.
func createDefaultConfig() component.Config {
	return &Config{
		Enabled:            true,
		AttributesToFetch:  []string{"command_line", "owner", "io", "executable_path"},
		StrictMode:         false,
		CriticalAttributes: []string{"process.executable.name", "process.command_line"},
		CacheTTLSeconds:    300, // 5 minutes default
		CommandLineCacheTTLSeconds: 300,   // 5 minutes default
		UsernameCacheTTLSeconds:    1800,  // 30 minutes default
		ExecutablePathCacheTTLSeconds: 3600, // 1 hour default
		CircuitBreakerEnabled:     true,
		CircuitBreakerThresholdMs: 100,  // 100ms default
		CircuitBreakerCooldownSeconds: 60, // 60 seconds default
	}
}