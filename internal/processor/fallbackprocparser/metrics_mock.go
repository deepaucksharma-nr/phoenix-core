package fallbackprocparser

import (
	"context"
	"sync"

	"github.com/deepaucksharma-nr/phoenix-core/internal/metrics"
	"go.uber.org/zap"
)

// mockMetricsRegistry implements a simple metrics registry for testing
type mockMetricsRegistry struct {
	logger           *zap.Logger
	counters         map[string]float64
	counterLabels    map[string]map[string]string
	histograms       map[string][]float64
	histogramLabels  map[string]map[string]string
	mu               sync.Mutex
}

// newMockMetricsRegistry creates a new mock metrics registry for testing
func newMockMetricsRegistry(logger *zap.Logger) *mockMetricsRegistry {
	return &mockMetricsRegistry{
		logger:           logger,
		counters:         make(map[string]float64),
		counterLabels:    make(map[string]map[string]string),
		histograms:       make(map[string][]float64),
		histogramLabels:  make(map[string]map[string]string),
		mu:               sync.Mutex{},
	}
}

// GetOrCreateCounter mocks the metrics registry GetOrCreateCounter method
func (m *mockMetricsRegistry) GetOrCreateCounter(name, help string, unit metrics.Unit) (metrics.Counter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	// Initialize counter if it doesn't exist
	if _, exists := m.counters[name]; !exists {
		m.counters[name] = 0
	}
	
	return &mockCounter{
		name:     name,
		registry: m,
	}, nil
}

// GetOrCreateHistogram mocks the metrics registry GetOrCreateHistogram method
func (m *mockMetricsRegistry) GetOrCreateHistogram(name, help string, unit metrics.Unit) (metrics.Histogram, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	// Initialize histogram if it doesn't exist
	if _, exists := m.histograms[name]; !exists {
		m.histograms[name] = []float64{}
	}
	
	return &mockHistogram{
		name:     name,
		registry: m,
	}, nil
}

// UpdateCounter mocks the metrics registry UpdateCounter method
func (m *mockMetricsRegistry) UpdateCounter(_ context.Context, name string, value float64, labels map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	// Initialize counter if it doesn't exist
	if _, exists := m.counters[name]; !exists {
		m.counters[name] = 0
	}
	
	// Update counter value
	m.counters[name] += value
	
	// Store the latest labels used
	m.counterLabels[name] = labels
	
	return nil
}

// UpdateHistogram mocks the metrics registry UpdateHistogram method
func (m *mockMetricsRegistry) UpdateHistogram(_ context.Context, name string, value float64, labels map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	// Initialize histogram if it doesn't exist
	if _, exists := m.histograms[name]; !exists {
		m.histograms[name] = []float64{}
	}
	
	// Add value to histogram
	m.histograms[name] = append(m.histograms[name], value)
	
	// Store the latest labels used
	m.histogramLabels[name] = labels
	
	return nil
}

// GetCounterValue returns the current value of a counter
func (m *mockMetricsRegistry) GetCounterValue(name string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if val, exists := m.counters[name]; exists {
		return val
	}
	return 0
}

// GetHistogramValues returns all recorded values for a histogram
func (m *mockMetricsRegistry) GetHistogramValues(name string) []float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if vals, exists := m.histograms[name]; exists {
		// Return a copy to avoid concurrent modification issues
		result := make([]float64, len(vals))
		copy(result, vals)
		return result
	}
	return []float64{}
}

// GetCounterLabels returns the latest labels used for a counter
func (m *mockMetricsRegistry) GetCounterLabels(name string) map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if labels, exists := m.counterLabels[name]; exists {
		return labels
	}
	return nil
}

// GetHistogramLabels returns the latest labels used for a histogram
func (m *mockMetricsRegistry) GetHistogramLabels(name string) map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if labels, exists := m.histogramLabels[name]; exists {
		return labels
	}
	return nil
}

// mockCounter implements a simple counter for testing
type mockCounter struct {
	name     string
	registry *mockMetricsRegistry
}

// Add implements the Counter interface Add method
func (c *mockCounter) Add(_ context.Context, value float64, labels map[string]string) error {
	return c.registry.UpdateCounter(context.Background(), c.name, value, labels)
}

// mockHistogram implements a simple histogram for testing
type mockHistogram struct {
	name     string
	registry *mockMetricsRegistry
}

// Record implements the Histogram interface Record method
func (h *mockHistogram) Record(_ context.Context, value float64, labels map[string]string) error {
	return h.registry.UpdateHistogram(context.Background(), h.name, value, labels)
}