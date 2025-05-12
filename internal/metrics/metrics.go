package metrics

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"
)

// MetricType defines what type the metric is.
type MetricType int

const (
	// Gauge represents a gauge metric.
	Gauge MetricType = iota
	// Counter represents a counter metric.
	Counter
	// Histogram represents a histogram metric.
	Histogram
)

// MetricsRegistry is a registry for custom PTE metrics.
type MetricsRegistry struct {
	sync.RWMutex
	meters         map[string]*MockMeterProvider // Using our mock implementation
	metricsMap     map[string]interface{}        // Map of metric names to their instances
	logger         *zap.Logger
	metricsBatches []pmetric.Metrics
}

// NewMetricsRegistry creates a new metrics registry.
func NewMetricsRegistry(logger *zap.Logger) *MetricsRegistry {
	return &MetricsRegistry{
		meters:     make(map[string]*MockMeterProvider),
		metricsMap: make(map[string]interface{}),
		logger:     logger,
	}
}

var (
	instance *MetricsRegistry
	once     sync.Once
)

// GetInstance returns the singleton instance of MetricsRegistry.
func GetInstance(logger *zap.Logger) *MetricsRegistry {
	once.Do(func() {
		instance = NewMetricsRegistry(logger)
	})
	return instance
}

// RegisterMeter registers a meter provider with the registry.
func (r *MetricsRegistry) RegisterMeter(name string, meter *MockMeterProvider) {
	r.Lock()
	defer r.Unlock()
	r.meters[name] = meter
}

// GetMeter returns a meter provider from the registry.
func (r *MetricsRegistry) GetMeter(name string) (*MockMeterProvider, bool) {
	r.RLock()
	defer r.RUnlock()
	meter, ok := r.meters[name]
	return meter, ok
}

// GetOrCreateGauge gets an existing gauge or creates a new one.
func (r *MetricsRegistry) GetOrCreateGauge(name, description, unit string) (pmetric.Gauge, error) {
	r.Lock()
	defer r.Unlock()
	
	// Check if already exists
	if metric, ok := r.metricsMap[name]; ok {
		return metric.(pmetric.Gauge), nil
	}
	
	// Create a new gauge
	metrics := pmetric.NewMetrics()
	metric := metrics.ResourceMetrics().AppendEmpty().ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName(name)
	metric.SetDescription(description)
	metric.SetUnit(unit)
	gauge := metric.SetEmptyGauge()
	
	// Store the batch and gauge
	r.metricsBatches = append(r.metricsBatches, metrics)
	r.metricsMap[name] = gauge
	
	return gauge, nil
}

// GetOrCreateCounter gets an existing counter or creates a new one.
func (r *MetricsRegistry) GetOrCreateCounter(name, description, unit string) (pmetric.Sum, error) {
	r.Lock()
	defer r.Unlock()
	
	// Check if already exists
	if metric, ok := r.metricsMap[name]; ok {
		return metric.(pmetric.Sum), nil
	}
	
	// Create a new counter
	metrics := pmetric.NewMetrics()
	metric := metrics.ResourceMetrics().AppendEmpty().ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName(name)
	metric.SetDescription(description)
	metric.SetUnit(unit)
	sum := metric.SetEmptySum()
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
	sum.SetIsMonotonic(true)
	
	// Store the batch and counter
	r.metricsBatches = append(r.metricsBatches, metrics)
	r.metricsMap[name] = sum
	
	return sum, nil
}

// GetOrCreateHistogram gets an existing histogram or creates a new one.
func (r *MetricsRegistry) GetOrCreateHistogram(name, description, unit string) (pmetric.Histogram, error) {
	r.Lock()
	defer r.Unlock()
	
	// Check if already exists
	if metric, ok := r.metricsMap[name]; ok {
		return metric.(pmetric.Histogram), nil
	}
	
	// Create a new histogram
	metrics := pmetric.NewMetrics()
	metric := metrics.ResourceMetrics().AppendEmpty().ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	metric.SetName(name)
	metric.SetDescription(description)
	metric.SetUnit(unit)
	histogram := metric.SetEmptyHistogram()
	histogram.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
	
	// Store the batch and histogram
	r.metricsBatches = append(r.metricsBatches, metrics)
	r.metricsMap[name] = histogram
	
	return histogram, nil
}

// UpdateGauge updates a gauge metric with the given value.
func (r *MetricsRegistry) UpdateGauge(ctx context.Context, name string, value float64, attributes map[string]string) error {
	r.RLock()
	defer r.RUnlock()
	
	metric, ok := r.metricsMap[name]
	if !ok {
		r.logger.Error("Failed to find gauge metric", zap.String("name", name))
		return nil
	}
	
	gauge := metric.(pmetric.Gauge)
	dp := gauge.DataPoints().AppendEmpty()
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	dp.SetDoubleValue(value)
	
	// Add attributes
	for k, v := range attributes {
		dp.Attributes().PutStr(k, v)
	}
	
	return nil
}

// UpdateCounter increments a counter metric by the given value.
func (r *MetricsRegistry) UpdateCounter(ctx context.Context, name string, value float64, attributes map[string]string) error {
	r.RLock()
	defer r.RUnlock()
	
	metric, ok := r.metricsMap[name]
	if !ok {
		r.logger.Error("Failed to find counter metric", zap.String("name", name))
		return nil
	}
	
	sum := metric.(pmetric.Sum)
	dp := sum.DataPoints().AppendEmpty()
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	dp.SetDoubleValue(value)
	
	// Add attributes
	for k, v := range attributes {
		dp.Attributes().PutStr(k, v)
	}
	
	return nil
}

// UpdateHistogram records a value to a histogram metric.
func (r *MetricsRegistry) UpdateHistogram(ctx context.Context, name string, value float64, attributes map[string]string) error {
	r.RLock()
	defer r.RUnlock()
	
	metric, ok := r.metricsMap[name]
	if !ok {
		r.logger.Error("Failed to find histogram metric", zap.String("name", name))
		return nil
	}
	
	histogram := metric.(pmetric.Histogram)
	dp := histogram.DataPoints().AppendEmpty()
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	dp.SetCount(1)
	dp.SetSum(value)
	
	// Add attributes
	for k, v := range attributes {
		dp.Attributes().PutStr(k, v)
	}
	
	return nil
}

// GetAllMetrics returns all collected metrics.
func (r *MetricsRegistry) GetAllMetrics() []pmetric.Metrics {
	r.RLock()
	defer r.RUnlock()
	return r.metricsBatches
}