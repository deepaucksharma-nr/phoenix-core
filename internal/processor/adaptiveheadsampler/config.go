package adaptiveheadsampler

import (
	"fmt"

	"go.opentelemetry.io/collector/component"
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
	// Simple string array for backward compatibility
	RecordIDFields []string `mapstructure:"record_id_fields"`

	// RecordIDFieldsWithWeight specifies the fields to hash with their relative importance
	// If both RecordIDFields and RecordIDFieldsWithWeight are specified, RecordIDFieldsWithWeight takes precedence
	RecordIDFieldsWithWeight []*RecordIDField `mapstructure:"record_id_fields_with_weight"`

	// ContentFilters specifies content-based filters for logs
	// If any filter matches, the log will be handled according to the filter action
	ContentFilters []*ContentFilter `mapstructure:"content_filters"`

	// CriticalLogPatterns defines patterns for critical logs that should always be sampled
	// regardless of the sampling rate. Each pattern is a string that can contain wildcards (*).
	// The pattern is applied to the specified field, and if matched, the log is always kept.
	// This is a special case of content-based filtering optimized for critical logs.
	CriticalLogPatterns []*CriticalLogPattern `mapstructure:"critical_log_patterns"`

	// TunableRegistryID is the ID to use when registering with the tunable registry
	// If not specified, defaults to "adaptive_head_sampler_traces" for traces and "adaptive_head_sampler_logs" for logs
	TunableRegistryID string `mapstructure:"tunable_registry_id"`
}

// FilterAction defines how to handle logs that match content filters
type FilterAction string

const (
	// ActionInclude ensures the log is always included (sampled)
	ActionInclude FilterAction = "include"
	// ActionExclude ensures the log is always excluded (not sampled)
	ActionExclude FilterAction = "exclude"
	// ActionUseWeightedProbability uses a different sampling probability for matching logs
	ActionUseWeightedProbability FilterAction = "weighted"
)

// ContentFilter defines a content-based filter for logs
type ContentFilter struct {
	// Field is the field to match against (supports dot notation for nested fields)
	Field string `mapstructure:"field"`

	// Pattern is the pattern to match (supports exact match and * as wildcard)
	Pattern string `mapstructure:"pattern"`

	// Action determines how to handle logs that match this filter
	Action FilterAction `mapstructure:"action"`

	// Probability is used when Action is "weighted" to override the sampling probability
	Probability float64 `mapstructure:"probability"`

	// Description optional human-readable description of the filter's purpose
	Description string `mapstructure:"description"`
}

// RecordIDField defines a field with weight for creating record IDs
type RecordIDField struct {
	// Field is the field path (supports dot notation for nested fields)
	Field string `mapstructure:"field"`

	// Weight is the relative importance of this field (higher values = more important)
	// Used for scaling the field's contribution to the overall hash
	// Default value is 1.0
	Weight float64 `mapstructure:"weight"`

	// Required indicates if this field must be present for the record to be sampled
	// If a required field is missing, the record will use the default value
	Required bool `mapstructure:"required"`

	// DefaultValue is used if the field is missing and Required is false
	DefaultValue string `mapstructure:"default_value"`
}

// CriticalLogPattern defines a pattern for identifying critical logs that should
// always be sampled, regardless of the sampling probability
type CriticalLogPattern struct {
	// Field is the field to match against (supports dot notation for nested fields)
	Field string `mapstructure:"field"`

	// Pattern is the pattern to match (supports exact match and * as wildcard)
	Pattern string `mapstructure:"pattern"`

	// Description is a human-readable description of what makes this log critical
	Description string `mapstructure:"description"`

	// SeverityLevel represents the criticality level of these log patterns (higher = more critical)
	// Useful for logging and metrics (default = 1)
	SeverityLevel int `mapstructure:"severity_level"`
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

	if cfg.HashSeedConfig == "RecordID" {
		// Check if we have any record ID fields configuration
		if len(cfg.RecordIDFields) == 0 && len(cfg.RecordIDFieldsWithWeight) == 0 {
			return fmt.Errorf("record_id_fields or record_id_fields_with_weight must be specified when hash_seed_config is 'RecordID'")
		}

		// Validate record_id_fields_with_weight if present
		for i, field := range cfg.RecordIDFieldsWithWeight {
			if field.Field == "" {
				return fmt.Errorf("record_id_fields_with_weight[%d]: field cannot be empty", i)
			}

			// Validate weights are positive
			if field.Weight < 0 {
				return fmt.Errorf("record_id_fields_with_weight[%d]: weight must be >= 0, got %f", i, field.Weight)
			}
		}
	}

	// Validate content filters
	for i, filter := range cfg.ContentFilters {
		if filter.Field == "" {
			return fmt.Errorf("content_filters[%d]: field cannot be empty", i)
		}

		if filter.Pattern == "" {
			return fmt.Errorf("content_filters[%d]: pattern cannot be empty", i)
		}

		if filter.Action == "" {
			return fmt.Errorf("content_filters[%d]: action cannot be empty", i)
		}

		if filter.Action != ActionInclude && filter.Action != ActionExclude && filter.Action != ActionUseWeightedProbability {
			return fmt.Errorf("content_filters[%d]: action must be 'include', 'exclude', or 'weighted', got %s",
				i, filter.Action)
		}

		if filter.Action == ActionUseWeightedProbability {
			if filter.Probability < 0 || filter.Probability > 1 {
				return fmt.Errorf("content_filters[%d]: probability must be between 0 and 1 when action is 'weighted', got %f",
					i, filter.Probability)
			}
		}
	}

	// Validate critical log patterns
	for i, pattern := range cfg.CriticalLogPatterns {
		if pattern.Field == "" {
			return fmt.Errorf("critical_log_patterns[%d]: field cannot be empty", i)
		}

		if pattern.Pattern == "" {
			return fmt.Errorf("critical_log_patterns[%d]: pattern cannot be empty", i)
		}

		// Set default severity level if not specified
		if pattern.SeverityLevel <= 0 {
			pattern.SeverityLevel = 1
		}
	}

	return nil
}

// CreateDefaultConfig creates the default configuration for the processor.
func createDefaultConfig() component.Config {
	return &Config{
		InitialProbability:       1.0, // By default, sample everything
		MinP:                     0.01,
		MaxP:                     1.0,
		HashSeedConfig:           "XORTraceID",
		ContentFilters:           []*ContentFilter{},       // Empty by default
		RecordIDFieldsWithWeight: []*RecordIDField{},       // Empty by default
		CriticalLogPatterns:      []*CriticalLogPattern{},  // Empty by default
		TunableRegistryID:        "",                       // Empty means use processor-specific defaults
	}
}