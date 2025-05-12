package fallbackprocparser

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/processor/processortest"
	"go.uber.org/zap/zaptest"
)

func TestCircuitBreakerConfig(t *testing.T) {
	// Test default values
	cfg := createDefaultConfig().(*Config)
	assert.True(t, cfg.CircuitBreakerEnabled, "Circuit breaker should be enabled by default")
	assert.Equal(t, 100, cfg.CircuitBreakerThresholdMs, "Default threshold should be 100ms")
	assert.Equal(t, 60, cfg.CircuitBreakerCooldownSeconds, "Default cooldown should be 60 seconds")

	// Test validation with valid values
	err := cfg.Validate()
	assert.NoError(t, err, "Valid config should not return error")

	// Test validation with invalid values
	cfgInvalid := createDefaultConfig().(*Config)
	cfgInvalid.CircuitBreakerThresholdMs = -10
	err = cfgInvalid.Validate()
	assert.Error(t, err, "Negative threshold should return error")
	assert.Contains(t, err.Error(), "circuit_breaker_threshold_ms must be >= 0")

	cfgInvalid = createDefaultConfig().(*Config)
	cfgInvalid.CircuitBreakerCooldownSeconds = -30
	err = cfgInvalid.Validate()
	assert.Error(t, err, "Negative cooldown should return error")
	assert.Contains(t, err.Error(), "circuit_breaker_cooldown_seconds must be >= 0")
}

func TestCircuitBreakerInitialization(t *testing.T) {
	// Create a config with circuit breaker enabled
	cfg := &Config{
		Enabled:                    true,
		CircuitBreakerEnabled:      true,
		CircuitBreakerThresholdMs:  100,
		CircuitBreakerCooldownSeconds: 60,
	}

	// Create processor
	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	require.NoError(t, err)
	require.NotNil(t, proc)

	// Verify circuit breaker enabled flag is set
	assert.True(t, proc.circuitBreakerEnabled, "Circuit breaker should be enabled")

	// Verify circuit breakers are initialized for all operation types
	operationTypes := []string{"cmdline", "username", "executable_path", "io"}
	for _, opType := range operationTypes {
		cbObj, exists := proc.circuitBreakers.Load(opType)
		assert.True(t, exists, "Circuit breaker for %s should be initialized", opType)
		
		cb, ok := cbObj.(*circuitBreaker)
		assert.True(t, ok, "Should be able to cast to circuitBreaker")
		assert.Equal(t, opType, cb.OperationType, "Operation type should match")
		assert.Equal(t, float64(100), cb.ThresholdMs, "Threshold should match config")
		assert.Equal(t, time.Duration(60)*time.Second, cb.CooldownPeriod, "Cooldown period should match config")
		assert.Equal(t, circuitStateClosed, cb.State.State, "Circuit should start in closed state")
	}

	// Create processor with circuit breaker disabled
	cfg.CircuitBreakerEnabled = false
	proc, err = newProcessor(processortest.NewNopCreateSettings(), cfg)
	require.NoError(t, err)
	require.NotNil(t, proc)

	// Verify circuit breaker disabled flag is set
	assert.False(t, proc.circuitBreakerEnabled, "Circuit breaker should be disabled")
}

func TestAllowOperation(t *testing.T) {
	// Create processor with circuit breaker enabled
	cfg := &Config{
		Enabled:                    true,
		CircuitBreakerEnabled:      true,
		CircuitBreakerThresholdMs:  100,
		CircuitBreakerCooldownSeconds: 60,
	}
	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	require.NoError(t, err)

	// Test when circuit breaker is closed (default state)
	allowed := proc.allowOperation("cmdline")
	assert.True(t, allowed, "Operation should be allowed when circuit is closed")

	// Set the circuit to open and test again
	cbObj, _ := proc.circuitBreakers.Load("cmdline")
	cb := cbObj.(*circuitBreaker)
	
	cb.Lock.Lock()
	cb.State.State = circuitStateOpen
	cb.State.UntilTime = time.Now().Add(10 * time.Second) // Open for 10 seconds
	cb.Lock.Unlock()

	// Test when circuit breaker is open and in cooldown period
	allowed = proc.allowOperation("cmdline")
	assert.False(t, allowed, "Operation should be blocked when circuit is open")

	// Test when circuit breaker is open but cooldown period has passed
	cb.Lock.Lock()
	cb.State.UntilTime = time.Now().Add(-1 * time.Second) // Set to past time
	cb.Lock.Unlock()

	allowed = proc.allowOperation("cmdline")
	assert.True(t, allowed, "Operation should be allowed after cooldown period")

	// Test with circuit breaker disabled
	proc.circuitBreakerEnabled = false
	allowed = proc.allowOperation("cmdline")
	assert.True(t, allowed, "Operation should always be allowed when circuit breaker is disabled")
}

func TestRecordOperationResult(t *testing.T) {
	// Create processor with circuit breaker enabled
	cfg := &Config{
		Enabled:                    true,
		CircuitBreakerEnabled:      true,
		CircuitBreakerThresholdMs:  100,
		CircuitBreakerCooldownSeconds: 1, // Short cooldown for testing
	}
	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	require.NoError(t, err)

	mockRegistry := newMockMetricsRegistry(zaptest.NewLogger(t))
	proc.metricsRegistry = mockRegistry

	// Test slow operation tracking
	for i := 0; i < 2; i++ {
		proc.recordOperationResult("cmdline", 150, true) // Over threshold
	}

	// Get the circuit breaker and check its state
	cbObj, _ := proc.circuitBreakers.Load("cmdline")
	cb := cbObj.(*circuitBreaker)
	
	cb.Lock.RLock()
	slowCount := cb.State.SlowOperationCount
	state := cb.State.State
	cb.Lock.RUnlock()
	
	assert.Equal(t, 2, slowCount, "Should have recorded 2 slow operations")
	assert.Equal(t, circuitStateClosed, state, "Circuit should still be closed")

	// Third slow operation should trip the circuit
	proc.recordOperationResult("cmdline", 200, true)
	
	cb.Lock.RLock()
	state = cb.State.State
	untilTime := cb.State.UntilTime
	cb.Lock.RUnlock()
	
	assert.Equal(t, circuitStateOpen, state, "Circuit should be open after 3 consecutive slow operations")
	assert.True(t, untilTime.After(time.Now()), "Cooldown period should be in the future")

	// Verify metric was recorded
	tripValue := mockRegistry.GetCounterValue("pte_fallback_parser_circuit_breaker_trip_total")
	assert.Equal(t, 1.0, tripValue, "Trip counter should be incremented")
	
	tripLabels := mockRegistry.GetCounterLabels("pte_fallback_parser_circuit_breaker_trip_total")
	assert.Equal(t, "cmdline", tripLabels["operation_type"], "Operation type label should be set")
	assert.Equal(t, ">1s", tripLabels["duration_tier"], "Duration tier should be set")

	// Test successful operation after circuit is open but cooldown has passed
	time.Sleep(1100 * time.Millisecond) // Wait for cooldown to expire
	
	// Circuit should allow a test operation now
	allowed := proc.allowOperation("cmdline")
	assert.True(t, allowed, "Circuit should allow operation after cooldown")
	
	// Record a successful operation within threshold
	proc.recordOperationResult("cmdline", 50, true)
	
	cb.Lock.RLock()
	state = cb.State.State
	slowCount = cb.State.SlowOperationCount
	cb.Lock.RUnlock()
	
	assert.Equal(t, circuitStateClosed, state, "Circuit should be closed after successful operation")
	assert.Equal(t, 0, slowCount, "Slow operation count should be reset")

	// Verify state change metric was recorded
	stateValue := mockRegistry.GetCounterValue("pte_fallback_parser_circuit_breaker_state_total")
	assert.Equal(t, 1.0, stateValue, "State change counter should be incremented")
	
	stateLabels := mockRegistry.GetCounterLabels("pte_fallback_parser_circuit_breaker_state_total")
	assert.Equal(t, "cmdline", stateLabels["operation_type"], "Operation type label should be set")
	assert.Equal(t, circuitStateClosed, stateLabels["state"], "State label should be set")
}

func TestCircuitBreakerWithMultipleOperations(t *testing.T) {
	// Create processor with circuit breaker enabled
	cfg := &Config{
		Enabled:                    true,
		CircuitBreakerEnabled:      true,
		CircuitBreakerThresholdMs:  100,
		CircuitBreakerCooldownSeconds: 1, // Short cooldown for testing
	}
	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	require.NoError(t, err)

	// Test that different operation types have independent circuit breakers
	for i := 0; i < 3; i++ {
		proc.recordOperationResult("cmdline", 150, true) // Over threshold
	}
	
	// Verify cmdline circuit was tripped
	cbCmdObj, _ := proc.circuitBreakers.Load("cmdline")
	cbCmd := cbCmdObj.(*circuitBreaker)
	
	cbCmd.Lock.RLock()
	cmdState := cbCmd.State.State
	cbCmd.Lock.RUnlock()
	
	assert.Equal(t, circuitStateOpen, cmdState, "Cmdline circuit should be open")
	
	// Check that username circuit is still closed
	cbUsernameObj, _ := proc.circuitBreakers.Load("username")
	cbUsername := cbUsernameObj.(*circuitBreaker)
	
	cbUsername.Lock.RLock()
	usernameState := cbUsername.State.State
	cbUsername.Lock.RUnlock()
	
	assert.Equal(t, circuitStateClosed, usernameState, "Username circuit should still be closed")
	
	// Operations should be allowed for username but not cmdline
	assert.False(t, proc.allowOperation("cmdline"), "Cmdline operations should be blocked")
	assert.True(t, proc.allowOperation("username"), "Username operations should be allowed")
}

func TestGetDurationTier(t *testing.T) {
	testCases := []struct {
		duration float64
		expected string
	}{
		{50.0, "<100ms"},
		{99.9, "<100ms"},
		{100.0, "100-500ms"},
		{250.0, "100-500ms"},
		{499.9, "100-500ms"},
		{500.0, "500-1000ms"},
		{750.0, "500-1000ms"},
		{999.9, "500-1000ms"},
		{1000.0, ">1s"},
		{2500.0, ">1s"},
	}
	
	for _, tc := range testCases {
		result := getDurationTier(tc.duration)
		assert.Equal(t, tc.expected, result, "Duration tier for %.1f should be %s", tc.duration, tc.expected)
	}
}

func TestCircuitBreakerConcurrency(t *testing.T) {
	// Create processor with circuit breaker enabled
	cfg := &Config{
		Enabled:                    true,
		CircuitBreakerEnabled:      true,
		CircuitBreakerThresholdMs:  100,
		CircuitBreakerCooldownSeconds: 1, // Short cooldown for testing
	}
	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	require.NoError(t, err)

	// Test concurrent access to circuit breakers
	const numGoroutines = 10
	const operationsPerGoroutine = 100
	
	var wg sync.WaitGroup
	wg.Add(numGoroutines)
	
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			
			// Each goroutine alternates between fast and slow operations
			for j := 0; j < operationsPerGoroutine; j++ {
				opType := "cmdline"
				if id%2 == 0 {
					opType = "username" // Use different operation types
				}
				
				// Check if operation is allowed
				allowed := proc.allowOperation(opType)
				
				if allowed {
					// Record operation result (alternate between fast and slow)
					duration := 50.0
					if j%3 == 0 {
						duration = 150.0 // Slow operation
					}
					proc.recordOperationResult(opType, duration, true)
				}
				
				// Small sleep to make test more realistic
				time.Sleep(time.Millisecond)
			}
		}(i)
	}
	
	wg.Wait() // Wait for all goroutines to complete
	
	// No assertions needed here - we're mainly testing that there are no race conditions
	// If there were issues with concurrent access, the test would likely panic
}