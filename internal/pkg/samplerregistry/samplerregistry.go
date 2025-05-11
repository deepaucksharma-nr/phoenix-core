package samplerregistry

import (
	"sync"
)

// ProbSampler is an interface for components that can have their sampling probability
// adjusted dynamically.
type ProbSampler interface {
	// SetProbability updates the sampler's probability value
	SetProbability(p float64)
	// GetProbability returns the current probability value
	GetProbability() float64
}

// Registry is a singleton registry for probabilistic samplers
type Registry struct {
	samplers sync.Map
}

var (
	instance *Registry
	once     sync.Once
)

// GetInstance returns the singleton instance of the registry
func GetInstance() *Registry {
	once.Do(func() {
		instance = &Registry{}
	})
	return instance
}

// Register adds a sampler to the registry with the given ID
func (r *Registry) Register(id string, sampler ProbSampler) {
	r.samplers.Store(id, sampler)
}

// Lookup retrieves a sampler by ID
func (r *Registry) Lookup(id string) (ProbSampler, bool) {
	if sampler, exists := r.samplers.Load(id); exists {
		return sampler.(ProbSampler), true
	}
	return nil, false
}

// ListSamplers returns a map of all registered samplers
func (r *Registry) ListSamplers() map[string]ProbSampler {
	result := make(map[string]ProbSampler)
	r.samplers.Range(func(key, value interface{}) bool {
		result[key.(string)] = value.(ProbSampler)
		return true
	})
	return result
}