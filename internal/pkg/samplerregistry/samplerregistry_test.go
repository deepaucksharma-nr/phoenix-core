package samplerregistry

import (
	"sync"
	"testing"
)

// mockSampler implements the ProbSampler interface for testing
type mockSampler struct {
	prob float64
	mu   sync.Mutex
}

func (m *mockSampler) SetProbability(p float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prob = p
}

func (m *mockSampler) GetProbability() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.prob
}

func TestSamplerRegistry(t *testing.T) {
	// Get singleton instance
	registry := GetInstance()

	// Create mock samplers
	sampler1 := &mockSampler{prob: 0.5}
	sampler2 := &mockSampler{prob: 0.25}

	// Register samplers
	registry.Register("sampler1", sampler1)
	registry.Register("sampler2", sampler2)

	// Test Lookup
	if s, exists := registry.Lookup("sampler1"); !exists || s != sampler1 {
		t.Errorf("Failed to lookup sampler1")
	}

	if s, exists := registry.Lookup("sampler2"); !exists || s != sampler2 {
		t.Errorf("Failed to lookup sampler2")
	}

	if _, exists := registry.Lookup("nonexistent"); exists {
		t.Errorf("Lookup returned true for nonexistent sampler")
	}

	// Test ListSamplers
	samplers := registry.ListSamplers()
	if len(samplers) != 2 {
		t.Errorf("Expected 2 samplers, got %d", len(samplers))
	}

	if samplers["sampler1"] != sampler1 || samplers["sampler2"] != sampler2 {
		t.Errorf("ListSamplers returned incorrect samplers")
	}

	// Test setting and getting probability
	sampler1.SetProbability(0.75)
	if sampler1.GetProbability() != 0.75 {
		t.Errorf("Expected probability 0.75, got %f", sampler1.GetProbability())
	}

	// Verify we can retrieve it from the registry and it's the same instance
	if s, exists := registry.Lookup("sampler1"); !exists || s.GetProbability() != 0.75 {
		t.Errorf("Retrieved sampler has incorrect probability")
	}
}