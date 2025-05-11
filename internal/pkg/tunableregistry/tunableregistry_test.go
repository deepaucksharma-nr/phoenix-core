package tunableregistry

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// mockTunable implements the Tunable interface for testing
type mockTunable struct {
	id     string
	values map[string]float64
	mu     sync.Mutex
}

func newMockTunable(id string) *mockTunable {
	return &mockTunable{
		id:     id,
		values: make(map[string]float64),
	}
}

func (m *mockTunable) SetValue(key string, value float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[key] = value
}

func (m *mockTunable) GetValue(key string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.values[key]
}

func (m *mockTunable) ID() string {
	return m.id
}

func TestTunableRegistry(t *testing.T) {
	// Get singleton instance
	registry := GetInstance()

	// Create mock tunables
	tunable1 := newMockTunable("tunable1")
	tunable2 := newMockTunable("tunable2")

	// Register tunables
	registry.Register(tunable1)
	registry.Register(tunable2)

	// Test Lookup
	if t1, exists := registry.Lookup("tunable1"); !exists || t1 != tunable1 {
		t.Errorf("Failed to lookup tunable1")
	}

	if t2, exists := registry.Lookup("tunable2"); !exists || t2 != tunable2 {
		t.Errorf("Failed to lookup tunable2")
	}

	if _, exists := registry.Lookup("nonexistent"); exists {
		t.Errorf("Lookup returned true for nonexistent tunable")
	}

	// Test SetValue/GetValue via registry
	registry.SetValue("tunable1", "param1", 0.5)
	registry.SetValue("tunable2", "param2", 0.25)

	val1, exists := registry.GetValue("tunable1", "param1")
	assert.True(t, exists)
	assert.Equal(t, 0.5, val1)

	val2, exists := registry.GetValue("tunable2", "param2")
	assert.True(t, exists)
	assert.Equal(t, 0.25, val2)

	// Test retrieving non-existent key
	val3, exists := registry.GetValue("tunable1", "nonexistent")
	assert.True(t, exists)
	assert.Equal(t, 0.0, val3) // Default value for non-existent key

	// Test setting/getting via tunable directly
	tunable1.SetValue("direct-param", 0.75)
	assert.Equal(t, 0.75, tunable1.GetValue("direct-param"))

	// Test ListTunables
	tunables := registry.ListTunables()
	assert.Equal(t, 2, len(tunables))
	assert.Equal(t, tunable1, tunables["tunable1"])
	assert.Equal(t, tunable2, tunables["tunable2"])
}