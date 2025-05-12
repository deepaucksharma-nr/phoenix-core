# TunableRegistry: Runtime Configuration Guide

## Overview

The `tunableregistry` package provides a flexible mechanism for runtime configurability of components in the Phoenix Telemetry Edge (PTE) system. This document explains how to use the tunableregistry to make your components dynamically configurable at runtime.

The `tunableregistry` package replaces the older `samplerregistry` package, offering a more generic approach that can be used for any component that needs to be dynamically configured, not just samplers.

## Why Use TunableRegistry?

The `tunableregistry` provides several advantages:

1. **Generic Interface**: Components can expose any number of numeric parameters using key-value pairs.
2. **Multiple Parameters**: A single component can expose multiple tunable parameters.
3. **Consistent Pattern**: All runtime-configurable components use the same interface and registry.
4. **Dynamic Configuration**: Parameters can be adjusted at runtime without restarting the service.
5. **PID Controller Integration**: Works seamlessly with the PID controller for automatic adaptive tuning.
6. **Standardized Approach**: Provides a common way to make components configurable across the system.

## Implementation Guide

### 1. Import the TunableRegistry Package

```go
import "github.com/deepaucksharma-nr/phoenix-core/internal/pkg/tunableregistry"
```

### 2. Implement the `Tunable` Interface

Your component should implement the Tunable interface:

```go
type MyComponent struct {
    // fields
    id string
    probability float64
    otherParam float64
}

// SetValue implements the tunableregistry.Tunable interface
func (c *MyComponent) SetValue(key string, value float64) {
    switch key {
    case "probability":
        c.probability = value
    case "other_param":
        c.otherParam = value
    }
    // Ignore unknown keys
}

// GetValue implements the tunableregistry.Tunable interface
func (c *MyComponent) GetValue(key string) float64 {
    switch key {
    case "probability":
        return c.probability
    case "other_param":
        return c.otherParam
    }
    // Return 0 for unknown keys
    return 0
}

// ID implements the tunableregistry.Tunable interface
func (c *MyComponent) ID() string {
    return c.id
}
```

### 3. Register with the TunableRegistry

Register your component with the tunableregistry singleton:

```go
// Get the registry singleton
registry := tunableregistry.GetInstance()

// Register your component
registry.Register(component)  // ID is retrieved from component.ID()
```

### 4. Configuration

Configure components to use the registry in your YAML configuration:

```yaml
extensions:
  pid_controller:
    interval: "15s"
    target_queue_utilization_high: 0.8
    target_queue_utilization_low: 0.2
    adjustment_factor_up: 1.25
    adjustment_factor_down: 0.8
    ewma_alpha: 0.3
    aggressive_drop_factor: 0.5
    aggressive_drop_window_count: 3
    metrics_endpoint: "http://localhost:8888/metrics"
    exporter_names: ["otlphttp/newrelic_default"]
    tunable_registry_id: "adaptive_head_sampler"
```

## Standardized Pattern for Registry ID

There are two approaches for setting the registry ID in PTE components:

### 1. Configurable Registry ID (Recommended)

For configurable components, include a `TunableRegistryID` field in your Config struct:

```go
type Config struct {
    // Other fields...

    // TunableRegistryID is the ID to use when registering as a tunable with the registry
    TunableRegistryID string `mapstructure:"tunable_registry_id"`
}
```

In your validation function:

```go
func (cfg *Config) Validate() error {
    // Other validation...

    // Check if registry ID is specified
    if cfg.TunableRegistryID == "" {
        return fmt.Errorf("tunable_registry_id must be specified")
    }

    return nil
}
```

And in the default configuration:

```go
func createDefaultConfig() component.Config {
    return &Config{
        // Other defaults...
        TunableRegistryID: "my_component_name",
    }
}
```

Use the registry ID in the component implementation:

```go
// ID implements the Tunable interface
func (c *MyComponent) ID() string {
    return c.config.TunableRegistryID
}
```

### 2. Hard-coded Registry ID

For simpler components or internal components, you can use a hard-coded registry ID:

```go
// In the component constructor
component := &MyComponent{
    // Other fields...
    registryID: "my_component_name",
}

// ID implements the Tunable interface
func (c *MyComponent) ID() string {
    return c.registryID
}
```

## Example Components

Several components in PTE already use the tunableregistry:

1. **Adaptive Head Sampler**: Exposes `probability` for dynamic sampling rate adjustment.
2. **Top-N Process Filter**: Exposes `top_n` to adjust how many processes are included.
3. **Reservoir Sampler**: Exposes `k_size` to adjust the reservoir size.
4. **PID Controller**: Uses the registry to adjust other components based on metrics.

## Migrating from SamplerRegistry

If you were previously using `samplerregistry.ProbSampler`, you should now use the `tunableregistry.Tunable` interface. Key differences:

1. `ProbSampler.SetProbability(p float64)` → `Tunable.SetValue("probability", p)`
2. `ProbSampler.GetProbability()` → `Tunable.GetValue("probability")`

## Need Help?

If you encounter any issues implementing the tunableregistry, please file an issue in our issue tracker.