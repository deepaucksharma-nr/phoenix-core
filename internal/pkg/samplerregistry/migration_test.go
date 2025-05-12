package samplerregistry

import (
	"testing"

	"github.com/deepaucksharma-nr/phoenix-core/internal/pkg/tunableregistry"
	"github.com/stretchr/testify/assert"
)

// mockProbSampler implements the ProbSampler interface for testing
type mockProbSampler struct {
	p float64
}

func (m *mockProbSampler) SetProbability(p float64) {
	m.p = p
}

func (m *mockProbSampler) GetProbability() float64 {
	return m.p
}

func TestProbSamplerToTunable(t *testing.T) {
	// Create a mock ProbSampler
	sampler := &mockProbSampler{p: 0.5}
	
	// Wrap it as a Tunable
	tunable := &ProbSamplerToTunable{
		IdString: "test_sampler",
		Sampler:  sampler,
	}
	
	// Test the ID method
	assert.Equal(t, "test_sampler", tunable.ID())
	
	// Test GetValue with "probability" key
	assert.Equal(t, 0.5, tunable.GetValue("probability"))
	
	// Test GetValue with unknown key
	assert.Equal(t, 0.0, tunable.GetValue("unknown_key"))
	
	// Test SetValue with "probability" key
	tunable.SetValue("probability", 0.75)
	assert.Equal(t, 0.75, sampler.p)
	assert.Equal(t, 0.75, tunable.GetValue("probability"))
	
	// Test SetValue with unknown key (should be ignored)
	tunable.SetValue("unknown_key", 1.0)
	assert.Equal(t, 0.75, sampler.p) // Should remain unchanged
}

func TestMigrateToTunableRegistry(t *testing.T) {
	// Clear both registries
	samplerReg := GetInstance()
	tunableReg := tunableregistry.GetInstance()
	
	// Create test samplers
	sampler1 := &mockProbSampler{p: 0.5}
	sampler2 := &mockProbSampler{p: 0.75}
	
	// Register with samplerregistry
	samplerReg.Register("sampler1", sampler1)
	samplerReg.Register("sampler2", sampler2)
	
	// Migrate to tunableregistry
	MigrateToTunableRegistry()
	
	// Check that samplers were properly migrated
	tunable1, exists1 := tunableReg.Lookup("sampler1")
	assert.True(t, exists1)
	assert.Equal(t, 0.5, tunable1.GetValue("probability"))
	
	tunable2, exists2 := tunableReg.Lookup("sampler2")
	assert.True(t, exists2)
	assert.Equal(t, 0.75, tunable2.GetValue("probability"))
	
	// Verify that changes propagate through both interfaces
	tunable1.SetValue("probability", 0.25)
	assert.Equal(t, 0.25, sampler1.GetProbability())
	
	sampler2.SetProbability(0.4)
	assert.Equal(t, 0.4, tunable2.GetValue("probability"))
}