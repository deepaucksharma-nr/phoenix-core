package tunableregistry

import (
	"sync"
)

// Tunable is an interface for components that support runtime value adjustments
type Tunable interface {
	// SetValue sets a value for the given key
	SetValue(key string, value float64)
	
	// GetValue gets the current value for the given key
	GetValue(key string) float64
	
	// ID returns the unique identifier for this tunable component
	ID() string
}

// Registry is a singleton registry for tunable components
type Registry struct {
	tunables sync.Map
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

// Register adds a tunable to the registry with the given ID
func (r *Registry) Register(tunable Tunable) {
	r.tunables.Store(tunable.ID(), tunable)
}

// Lookup retrieves a tunable by ID
func (r *Registry) Lookup(id string) (Tunable, bool) {
	if tunable, exists := r.tunables.Load(id); exists {
		return tunable.(Tunable), true
	}
	return nil, false
}

// SetValue sets a value for a key on a tunable identified by ID
func (r *Registry) SetValue(id string, key string, value float64) bool {
	if tunable, exists := r.Lookup(id); exists {
		tunable.SetValue(key, value)
		return true
	}
	return false
}

// GetValue gets a value for a key from a tunable identified by ID
func (r *Registry) GetValue(id string, key string) (float64, bool) {
	if tunable, exists := r.Lookup(id); exists {
		return tunable.GetValue(key), true
	}
	return 0, false
}

// ListTunables returns a map of all registered tunables
func (r *Registry) ListTunables() map[string]Tunable {
	result := make(map[string]Tunable)
	r.tunables.Range(func(key, value interface{}) bool {
		result[key.(string)] = value.(Tunable)
		return true
	})
	return result
}