// Package samplerregistry is a registry for probabilistic samplers.
// Deprecated: Use tunableregistry instead, which provides a more generic mechanism for runtime configurability.
package samplerregistry

import (
	"sync"

	"github.com/deepaucksharma-nr/phoenix-core/internal/pkg/tunableregistry"
)

// ProbSampler is an interface for components that can have their sampling probability
// adjusted dynamically.
//
// Deprecated: Use tunableregistry.Tunable instead, which provides a more generic
// approach by supporting multiple configurable parameters via key-value pairs.
type ProbSampler interface {
	// SetProbability updates the sampler's probability value
	SetProbability(p float64)
	// GetProbability returns the current probability value
	GetProbability() float64
}

// Registry is a singleton registry for probabilistic samplers
//
// Deprecated: Use tunableregistry.Registry instead, which provides a more generic
// registry for runtime tunable components.
type Registry struct {
	samplers sync.Map
}

var (
	instance *Registry
	once     sync.Once
)

// GetInstance returns the singleton instance of the registry
//
// Deprecated: Use tunableregistry.GetInstance instead, which provides a more generic
// registry for runtime tunable components.
func GetInstance() *Registry {
	once.Do(func() {
		instance = &Registry{}
	})
	return instance
}

// Register adds a sampler to the registry with the given ID
//
// Deprecated: Use tunableregistry.Registry.Register instead, which registers a
// tunable component using its ID() method.
func (r *Registry) Register(id string, sampler ProbSampler) {
	r.samplers.Store(id, sampler)
}

// Lookup retrieves a sampler by ID
//
// Deprecated: Use tunableregistry.Registry.Lookup instead, which returns a
// tunable component by its ID.
func (r *Registry) Lookup(id string) (ProbSampler, bool) {
	if sampler, exists := r.samplers.Load(id); exists {
		return sampler.(ProbSampler), true
	}
	return nil, false
}

// ListSamplers returns a map of all registered samplers
//
// Deprecated: Use tunableregistry.Registry.ListTunables instead, which returns
// a map of all registered tunable components.
func (r *Registry) ListSamplers() map[string]ProbSampler {
	result := make(map[string]ProbSampler)
	r.samplers.Range(func(key, value interface{}) bool {
		result[key.(string)] = value.(ProbSampler)
		return true
	})
	return result
}

// ProbSamplerToTunable wraps a ProbSampler implementation to conform to
// the tunableregistry.Tunable interface.
//
// This is a helper function for migrating existing ProbSampler implementations
// to use the tunableregistry package instead.
type ProbSamplerToTunable struct {
	IdString string
	Sampler  ProbSampler
}

// SetValue implements the tunableregistry.Tunable interface.
func (p *ProbSamplerToTunable) SetValue(key string, value float64) {
	if key == "probability" {
		p.Sampler.SetProbability(value)
	}
	// Ignore other keys
}

// GetValue implements the tunableregistry.Tunable interface.
func (p *ProbSamplerToTunable) GetValue(key string) float64 {
	if key == "probability" {
		return p.Sampler.GetProbability()
	}
	// Return 0 for unknown keys
	return 0
}

// ID implements the tunableregistry.Tunable interface.
func (p *ProbSamplerToTunable) ID() string {
	return p.IdString
}

// MigrateToTunableRegistry copies all registered samplers from the samplerregistry
// to the tunableregistry, wrapping them as tunableregistry.Tunable implementations.
//
// This is a helper function for migrating existing applications from samplerregistry
// to tunableregistry.
func MigrateToTunableRegistry() {
	// Get instances of both registries
	samplerReg := GetInstance()
	tunableReg := tunableregistry.GetInstance()

	// Get all registered samplers
	samplers := samplerReg.ListSamplers()

	// Register each sampler as a tunable
	for id, sampler := range samplers {
		tunable := &ProbSamplerToTunable{
			IdString: id,
			Sampler:  sampler,
		}
		tunableReg.Register(tunable)
	}
}