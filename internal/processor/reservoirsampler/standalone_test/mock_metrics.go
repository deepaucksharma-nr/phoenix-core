package standalone_test

import (
	"context"
	"sync"

	"go.uber.org/zap"
)

// MockMetricsRegistry is a simple metrics registry for testing
type MockMetricsRegistry struct {
	Gauges     map[string]float64
	Counters   map[string]float64
	Histograms map[string][]float64
	lock       sync.Mutex
	logger     *zap.Logger
}

// NewMockMetricsRegistry creates a new mock metrics registry
func NewMockMetricsRegistry(logger *zap.Logger) *MockMetricsRegistry {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &MockMetricsRegistry{
		Gauges:     make(map[string]float64),
		Counters:   make(map[string]float64),
		Histograms: make(map[string][]float64),
		logger:     logger,
	}
}

// GetOrCreateGauge gets or creates a gauge
func (m *MockMetricsRegistry) GetOrCreateGauge(name, desc, unit string) (interface{}, error) {
	m.lock.Lock()
	defer m.lock.Unlock()
	
	if _, exists := m.Gauges[name]; !exists {
		m.Gauges[name] = 0
	}
	return name, nil
}

// GetOrCreateCounter gets or creates a counter
func (m *MockMetricsRegistry) GetOrCreateCounter(name, desc, unit string) (interface{}, error) {
	m.lock.Lock()
	defer m.lock.Unlock()
	
	if _, exists := m.Counters[name]; !exists {
		m.Counters[name] = 0
	}
	return name, nil
}

// GetOrCreateHistogram gets or creates a histogram
func (m *MockMetricsRegistry) GetOrCreateHistogram(name, desc, unit string) (interface{}, error) {
	m.lock.Lock()
	defer m.lock.Unlock()
	
	if _, exists := m.Histograms[name]; !exists {
		m.Histograms[name] = make([]float64, 0)
	}
	return name, nil
}

// UpdateGauge updates a gauge value
func (m *MockMetricsRegistry) UpdateGauge(ctx context.Context, name string, value float64, attrs map[string]string) error {
	m.lock.Lock()
	defer m.lock.Unlock()
	
	m.Gauges[name] = value
	return nil
}

// UpdateCounter updates a counter value
func (m *MockMetricsRegistry) UpdateCounter(ctx context.Context, name string, value float64, attrs map[string]string) error {
	m.lock.Lock()
	defer m.lock.Unlock()
	
	m.Counters[name] += value
	return nil
}

// UpdateHistogram updates a histogram value
func (m *MockMetricsRegistry) UpdateHistogram(ctx context.Context, name string, value float64, attrs map[string]string) error {
	m.lock.Lock()
	defer m.lock.Unlock()
	
	m.Histograms[name] = append(m.Histograms[name], value)
	return nil
}

// GetGaugeValue gets the current value of a gauge
func (m *MockMetricsRegistry) GetGaugeValue(name string) float64 {
	m.lock.Lock()
	defer m.lock.Unlock()
	
	return m.Gauges[name]
}

// GetCounterValue gets the current value of a counter
func (m *MockMetricsRegistry) GetCounterValue(name string) float64 {
	m.lock.Lock()
	defer m.lock.Unlock()
	
	return m.Counters[name]
}

// GetHistogramValues gets the current values in a histogram
func (m *MockMetricsRegistry) GetHistogramValues(name string) []float64 {
	m.lock.Lock()
	defer m.lock.Unlock()
	
	return m.Histograms[name]
}