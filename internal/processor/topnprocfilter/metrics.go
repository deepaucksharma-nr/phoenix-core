package topnprocfilter

import (
	"time"
)

// Constants for metrics reporting optimization
const (
	defaultMetricsReportingInterval = 10 * time.Second
	defaultMetricsBufferSize        = 100
	defaultMetricsBatchSize         = 10
	circuitBreakerThreshold         = 5   // Number of consecutive errors before opening circuit
	circuitBreakerResetInterval     = 30  // Seconds before trying to close circuit again
)

// metricUpdate represents a metric update operation to be processed
type metricUpdate struct {
	metricName string
	value      float64
	attrs      map[string]string
	metricType string // "gauge" or "counter"
}