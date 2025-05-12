package pidcontroller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/deepaucksharma-nr/phoenix-core/internal/pkg/tunableregistry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.uber.org/zap"
)

// mockTunable implements the Tunable interface for testing
type mockTunable struct {
	prob float64
	id   string
}

func (m *mockTunable) SetValue(key string, value float64) {
	if key == "probability" {
		m.prob = value
	}
}

func (m *mockTunable) GetValue(key string) float64 {
	if key == "probability" {
		return m.prob
	}
	return 0.0
}

func (m *mockTunable) ID() string {
	return m.id
}

func TestPIDController(t *testing.T) {
	// Create a mock metrics server that returns queue metrics
	highUtilServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Respond with metrics indicating high queue utilization
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`
# HELP otelcol_exporter_queue_size Current size of the retry queue (in units).
# TYPE otelcol_exporter_queue_size gauge
otelcol_exporter_queue_size{exporter="otlphttp/newrelic_default"} 800
# HELP otelcol_exporter_queue_capacity Fixed capacity of the retry queue (in units).
# TYPE otelcol_exporter_queue_capacity gauge
otelcol_exporter_queue_capacity{exporter="otlphttp/newrelic_default"} 1000
`))
	}))
	defer highUtilServer.Close()

	lowUtilServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Respond with metrics indicating low queue utilization
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`
# HELP otelcol_exporter_queue_size Current size of the retry queue (in units).
# TYPE otelcol_exporter_queue_size gauge
otelcol_exporter_queue_size{exporter="otlphttp/newrelic_default"} 100
# HELP otelcol_exporter_queue_capacity Fixed capacity of the retry queue (in units).
# TYPE otelcol_exporter_queue_capacity gauge
otelcol_exporter_queue_capacity{exporter="otlphttp/newrelic_default"} 1000
`))
	}))
	defer lowUtilServer.Close()

	// Register a mock tunable
	mockSamp := &mockTunable{prob: 0.5, id: "adaptive_head_sampler"}
	registry := tunableregistry.GetInstance()
	registry.Register(mockSamp)
	
	// Create a PID controller with high utilization
	highUtilConfig := &Config{
		Interval:                   "100ms", // Short interval for testing
		TargetQueueUtilizationHigh: 0.8,
		TargetQueueUtilizationLow:  0.2,
		AdjustmentFactorUp:         1.25,
		AdjustmentFactorDown:       0.8,
		EWMAAlpha:                  1.0, // No smoothing for testing
		AggressiveDropFactor:       0.5,
		AggressiveDropWindowCount:  3,
		MetricsEndpoint:            highUtilServer.URL,
		ExporterNames:              []string{"otlphttp/newrelic_default"},
		TunableRegistryID:          "adaptive_head_sampler",
	}
	
	set := extension.CreateSettings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
		BuildInfo: component.BuildInfo{},
	}
	
	highUtilController, err := newPIDController(set, highUtilConfig)
	require.NoError(t, err)
	
	// Start the controller
	err = highUtilController.Start(context.Background(), nil)
	require.NoError(t, err)
	
	// Wait for a control cycle
	time.Sleep(150 * time.Millisecond)
	
	// Check that probability was decreased
	assert.Less(t, mockSamp.GetValue("probability"), 0.5, "Probability should decrease due to high utilization")
	
	// Stop the controller
	err = highUtilController.Shutdown(context.Background())
	require.NoError(t, err)
	
	// Reset the sampler
	mockSamp.SetValue("probability", 0.5)
	
	// Create a PID controller with low utilization
	lowUtilConfig := &Config{
		Interval:                   "100ms", // Short interval for testing
		TargetQueueUtilizationHigh: 0.8,
		TargetQueueUtilizationLow:  0.2,
		AdjustmentFactorUp:         1.25,
		AdjustmentFactorDown:       0.8,
		EWMAAlpha:                  1.0, // No smoothing for testing
		AggressiveDropFactor:       0.5,
		AggressiveDropWindowCount:  3,
		MetricsEndpoint:            lowUtilServer.URL,
		ExporterNames:              []string{"otlphttp/newrelic_default"},
		TunableRegistryID:          "adaptive_head_sampler",
	}
	
	lowUtilController, err := newPIDController(set, lowUtilConfig)
	require.NoError(t, err)
	
	// Start the controller
	err = lowUtilController.Start(context.Background(), nil)
	require.NoError(t, err)
	
	// Wait for a control cycle
	time.Sleep(150 * time.Millisecond)
	
	// Check that probability was increased
	assert.Greater(t, mockSamp.GetValue("probability"), 0.5, "Probability should increase due to low utilization")
	
	// Stop the controller
	err = lowUtilController.Shutdown(context.Background())
	require.NoError(t, err)
}

func TestAggressiveDrop(t *testing.T) {
	// Create a mock metrics server that returns queue metrics
	highUtilServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Respond with metrics indicating very high queue utilization
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`
# HELP otelcol_exporter_queue_size Current size of the retry queue (in units).
# TYPE otelcol_exporter_queue_size gauge
otelcol_exporter_queue_size{exporter="otlphttp/newrelic_default"} 900
# HELP otelcol_exporter_queue_capacity Fixed capacity of the retry queue (in units).
# TYPE otelcol_exporter_queue_capacity gauge
otelcol_exporter_queue_capacity{exporter="otlphttp/newrelic_default"} 1000
`))
	}))
	defer highUtilServer.Close()

	// Register a mock tunable
	mockSamp := &mockTunable{prob: 0.5, id: "adaptive_head_sampler"}
	registry := tunableregistry.GetInstance()
	registry.Register(mockSamp)
	
	// Create a PID controller with aggressive drop enabled
	config := &Config{
		Interval:                   "50ms", // Short interval for testing
		TargetQueueUtilizationHigh: 0.8,
		TargetQueueUtilizationLow:  0.2,
		AdjustmentFactorUp:         1.25,
		AdjustmentFactorDown:       0.8,
		EWMAAlpha:                  1.0, // No smoothing for testing
		AggressiveDropFactor:       0.5, // 50% reduction
		AggressiveDropWindowCount:  2,   // After 2 cycles
		MetricsEndpoint:            highUtilServer.URL,
		ExporterNames:              []string{"otlphttp/newrelic_default"},
		TunableRegistryID:          "adaptive_head_sampler",
	}
	
	set := extension.CreateSettings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
		BuildInfo: component.BuildInfo{},
	}
	
	controller, err := newPIDController(set, config)
	require.NoError(t, err)
	
	// Start the controller
	err = controller.Start(context.Background(), nil)
	require.NoError(t, err)
	
	// Initial probability
	initialP := mockSamp.GetValue("probability")

	// Wait for first cycle (normal adjustment)
	time.Sleep(60 * time.Millisecond)

	// Check probability decreased by normal factor
	firstP := mockSamp.GetValue("probability")
	assert.InDelta(t, initialP*0.8, firstP, 0.01, "First adjustment should use normal factor")

	// Wait for second cycle (normal adjustment)
	time.Sleep(60 * time.Millisecond)

	// Check probability decreased by normal factor again
	secondP := mockSamp.GetValue("probability")
	assert.InDelta(t, firstP*0.8, secondP, 0.01, "Second adjustment should use normal factor")

	// Wait for third cycle (aggressive adjustment)
	time.Sleep(60 * time.Millisecond)

	// Check probability decreased by aggressive factor
	thirdP := mockSamp.GetValue("probability")
	assert.InDelta(t, secondP*0.5, thirdP, 0.01, "Third adjustment should use aggressive factor")
	
	// Stop the controller
	err = controller.Shutdown(context.Background())
	require.NoError(t, err)
}