# Migrating from SamplerRegistry to TunableRegistry

## Overview

The `samplerregistry` package is being deprecated in favor of the more generic `tunableregistry` package, which provides a more flexible mechanism for runtime configurability. This document provides guidance on how to migrate your code from using `samplerregistry` to `tunableregistry`.

## Why Migrate?

The `tunableregistry` provides several advantages over the older `samplerregistry`:

1. **Generic Interface**: Rather than being limited to sampling probability, the `tunableregistry` allows configuring any numeric parameter using key-value pairs.
2. **Multiple Parameters**: A single component can expose multiple tunable parameters.
3. **Consistent Pattern**: All runtime-configurable components use the same interface and registry.
4. **Future Compatibility**: New features and improvements will be added to the `tunableregistry` only.

## Migration Steps

### 1. Change Your Import Statements

Replace:
```go
import "github.com/deepaucksharma-nr/phoenix-core/internal/pkg/samplerregistry"
```

With:
```go
import "github.com/deepaucksharma-nr/phoenix-core/internal/pkg/tunableregistry"
```

### 2. Implement the `Tunable` Interface

Instead of implementing the `ProbSampler` interface, implement the `Tunable` interface:

```go
// Before
type MyComponent struct {
    // fields
}

func (c *MyComponent) SetProbability(p float64) {
    // implementation
}

func (c *MyComponent) GetProbability() float64 {
    // implementation
}

// After
type MyComponent struct {
    // fields
    id string
}

func (c *MyComponent) SetValue(key string, value float64) {
    if key == "probability" {
        // Same implementation as old SetProbability
    }
    // Handle other keys if needed
}

func (c *MyComponent) GetValue(key string) float64 {
    if key == "probability" {
        // Same implementation as old GetProbability
    }
    // Handle other keys, return 0 for unknown keys
    return 0
}

func (c *MyComponent) ID() string {
    return c.id
}
```

### 3. Register with the `tunableregistry`

Change your registration code:

```go
// Before
registry := samplerregistry.GetInstance()
registry.Register("my-component", component)

// After
registry := tunableregistry.GetInstance()
registry.Register(component)  // ID is now retrieved from the component itself
```

### 4. Updating Configuration

If you have configuration using `sampler_registry_id`, update to use `tunable_registry_id` instead.

```yaml
# Before
sampler_registry_id: "my-component"

# After
tunable_registry_id: "my-component"
```

### 5. For Existing Components

If you have existing components implementing `ProbSampler` and can't immediately rewrite them, you can use the provided adapter:

```go
// Create a wrapper for your existing ProbSampler
sampler := &existingComponent{}
tunable := &samplerregistry.ProbSamplerToTunable{
    IdString: "my-component",
    Sampler:  sampler,
}

// Register with tunableregistry
registry := tunableregistry.GetInstance()
registry.Register(tunable)
```

### 6. Migrating an Entire Application

For applications with many components registered in `samplerregistry`, you can use the helper function to migrate all at once:

```go
// This will copy all registered samplers from samplerregistry to tunableregistry
samplerregistry.MigrateToTunableRegistry()
```

## Example: PID Controller Configuration

The PID controller has been updated to support both `sampler_registry_id` and `tunable_registry_id`, with preference given to `tunable_registry_id`:

```yaml
extensions:
  pid_controller:
    # Use this (preferred)
    tunable_registry_id: "adaptive_head_sampler"
    
    # Or this (deprecated, but still supported)
    sampler_registry_id: "adaptive_head_sampler"
```

## Timeline

- **Current**: Both registries are supported, with `samplerregistry` marked as deprecated.
- **Future**: The `samplerregistry` will be removed in a future major version.

## Need Help?

If you encounter any issues during migration, please file an issue in our issue tracker.