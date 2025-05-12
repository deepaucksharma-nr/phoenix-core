package reservoirsampler

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
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

	// DbCompactionScheduleCron is a cron expression that specifies when to run BoltDB compaction
	// e.g., "0 2 * * 0" for every Sunday at 2am.
	// When empty, no automatic compaction will be performed.
	DbCompactionScheduleCron string `mapstructure:"db_compaction_schedule_cron"`

	// DbCompactionTargetSize is the target size factor for compaction. When compacting,
	// if the DB is larger than (FullSpanLruSize * DbCompactionTargetSize) bytes, it will be compacted.
	// Defaults to 10MB.
	DbCompactionTargetSize int64 `mapstructure:"db_compaction_target_size"`

	// TunableRegistryID is the ID to use when registering with the tunable registry
	// If not specified, defaults to "reservoir_sampler"
	TunableRegistryID string `mapstructure:"tunable_registry_id"`

	// TraceAware determines whether to use trace-aware sampling.
	// When true, spans from the same trace are buffered and sampled together.
	// This ensures that if any span from a trace is sampled, all spans from that
	// trace are sampled together, which provides more coherent trace data for analysis.
	//
	// Key benefits:
	// - Maintains trace integrity (all spans from a trace stay together)
	// - Produces more representative trace samples
	// - Reduces partial traces in exports
	//
	// If false, each span is sampled individually using standard reservoir sampling,
	// which may result in partial traces being exported.
	TraceAware bool `mapstructure:"trace_aware"`

	// TraceTimeout is the maximum time to wait for a trace to complete before sampling it.
	// Specified as a duration string (e.g., "5s", "500ms").
	// Defaults to "5s" when not specified.
	//
	// This parameter is only used when TraceAware is true. It controls how long to wait
	// for additional spans to arrive before considering a trace ready for sampling.
	// When a trace has been in the buffer for longer than this timeout, it will be
	// eligible for sampling even if it's not yet complete (i.e., has no root span).
	//
	// This prevents memory leaks from abandoned traces and ensures timely processing.
	// Setting this too low might result in more fragmented traces, while setting it
	// too high might increase memory usage and delay sampling decisions.
	TraceTimeout string `mapstructure:"trace_timeout"`
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

	// Validate cron schedule if provided
	if cfg.DbCompactionScheduleCron != "" {
		_, err := cron.ParseStandard(cfg.DbCompactionScheduleCron)
		if err != nil {
			return fmt.Errorf("invalid db_compaction_schedule_cron format: %w", err)
		}
	}

	// Validate compaction target size
	if cfg.DbCompactionTargetSize <= 0 {
		return fmt.Errorf("db_compaction_target_size must be greater than 0, got %d", cfg.DbCompactionTargetSize)
	}

	// Validate trace timeout format if provided
	if cfg.TraceTimeout != "" {
		_, err := time.ParseDuration(cfg.TraceTimeout)
		if err != nil {
			return fmt.Errorf("invalid trace_timeout format: %w", err)
		}
	}

	return nil
}

// CreateDefaultConfig creates the default configuration for the processor.
func createDefaultConfig() component.Config {
	return &Config{
		SizeK:                    5000,
		WindowDuration:           "60s",
		CheckpointPath:           "/data/pte/reservoir_state.db",
		CheckpointInterval:       "10s",
		DbCompactionScheduleCron: "0 2 * * 0",         // Sunday at 2am
		DbCompactionTargetSize:   10 * 1024 * 1024,    // 10MB
		TunableRegistryID:        "reservoir_sampler", // Default registry ID
		TraceAware:               true,
		TraceTimeout:             "5s",
	}
}
