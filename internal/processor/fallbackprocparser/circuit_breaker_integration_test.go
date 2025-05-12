package fallbackprocparser

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/processor/processortest"
	"go.uber.org/zap/zaptest"
)

// TestCircuitBreakerInCommandLineFetch tests the integration of circuit breakers with the
// command line fetch function.
func TestCircuitBreakerInCommandLineFetch(t *testing.T) {
	// Create a mock proc filesystem
	mockProc := newMockProcFS(t)
	defer mockProc.Cleanup()

	// Add test processes
	mockProc.AddProcess(1001, WithCmdline("test command line"))
	mockProc.AddProcess(1002, WithAccessErrors()) // Will cause errors when accessed

	// Create processor with circuit breaker enabled and low threshold
	logger := zaptest.NewLogger(t)
	cfg := &Config{
		Enabled:                     true,
		AttributesToFetch:           []string{"command_line"},
		CircuitBreakerEnabled:       true,
		CircuitBreakerThresholdMs:   5, // Very low threshold to trigger circuit breaker easily
		CircuitBreakerCooldownSeconds: 1, // Short cooldown for faster testing
	}

	// Create a processor
	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	require.NoError(t, err)

	// Replace the fetchCommandLine function with a test version that can be slow
	originalFetchCmdline := proc.fetchCommandLine
	defer func() {
		proc.fetchCommandLine = originalFetchCmdline
	}()

	var slowCount int
	proc.fetchCommandLine = func(pid int) (string, error) {
		// PID 1001 will be successful but slow
		if pid == 1001 {
			// Slow operation
			if slowCount < 3 {
				slowCount++
				time.Sleep(10 * time.Millisecond) // Exceed the threshold
				return "test command line", nil
			}
			// After 3 slow operations, return faster
			return "test command line", nil
		}
		
		// PID 1002 will have errors
		if pid == 1002 {
			return "", errors.New("error reading cmdline")
		}
		
		return "", fmt.Errorf("unknown pid %d", pid)
	}

	// First attempt should work but be slow
	cmdline, err := proc.fetchCommandLine(1001)
	assert.NoError(t, err)
	assert.Equal(t, "test command line", cmdline)

	// Second attempt should work but be slow
	cmdline, err = proc.fetchCommandLine(1001)
	assert.NoError(t, err)
	assert.Equal(t, "test command line", cmdline)

	// Third slow attempt should work but trip the circuit breaker
	cmdline, err = proc.fetchCommandLine(1001)
	assert.NoError(t, err)
	assert.Equal(t, "test command line", cmdline)

	// Now the circuit breaker should be open
	cbObj, _ := proc.circuitBreakers.Load("cmdline")
	cb := cbObj.(*circuitBreaker)
	
	cb.Lock.RLock()
	state := cb.State.State
	cb.Lock.RUnlock()
	
	assert.Equal(t, circuitStateOpen, state, "Circuit should be open after 3 consecutive slow operations")

	// Subsequent attempt should be blocked by circuit breaker
	cmdline, err = proc.fetchCommandLine(1001)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "operation blocked by circuit breaker")
	assert.Empty(t, cmdline)

	// Wait for the cooldown to expire
	time.Sleep(1100 * time.Millisecond)

	// Now it should work again
	cmdline, err = proc.fetchCommandLine(1001)
	assert.NoError(t, err)
	assert.Equal(t, "test command line", cmdline)

	// Verify the circuit is closed again
	cb.Lock.RLock()
	state = cb.State.State
	cb.Lock.RUnlock()
	
	assert.Equal(t, circuitStateClosed, state, "Circuit should be closed after a successful operation")
}

// TestCircuitBreakerInUsernameFetch tests the integration of circuit breakers with the
// username resolution function.
func TestCircuitBreakerInUsernameFetch(t *testing.T) {
	// Create processor with circuit breaker enabled and low threshold
	logger := zaptest.NewLogger(t)
	cfg := &Config{
		Enabled:                     true,
		AttributesToFetch:           []string{"owner"},
		CircuitBreakerEnabled:       true,
		CircuitBreakerThresholdMs:   5, // Very low threshold to trigger circuit breaker easily
		CircuitBreakerCooldownSeconds: 1, // Short cooldown for faster testing
	}

	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	require.NoError(t, err)

	// Mock the resolveUIDToUsername function
	originalResolveUid := proc.resolveUIDToUsername
	defer func() {
		proc.resolveUIDToUsername = originalResolveUid
	}()

	var slowCount int
	proc.resolveUIDToUsername = func(uid string) (string, error) {
		if uid == "1000" {
			// Slow operation
			if slowCount < 3 {
				slowCount++
				time.Sleep(10 * time.Millisecond) // Exceed the threshold
				return "testuser", nil
			}
			// After 3 slow operations, return faster
			return "testuser", nil
		} else if uid == "1001" {
			return "", errors.New("user not found")
		}
		
		return "", fmt.Errorf("unknown uid %s", uid)
	}

	// First three attempts should work but be slow
	for i := 0; i < 3; i++ {
		username, err := proc.resolveUIDToUsername("1000")
		assert.NoError(t, err)
		assert.Equal(t, "testuser", username)
	}

	// Now the circuit breaker should be open
	cbObj, _ := proc.circuitBreakers.Load("username")
	cb := cbObj.(*circuitBreaker)
	
	cb.Lock.RLock()
	state := cb.State.State
	cb.Lock.RUnlock()
	
	assert.Equal(t, circuitStateOpen, state, "Circuit should be open after 3 consecutive slow operations")

	// Username resolution should return a fallback when blocked by circuit breaker
	username, err := proc.resolveUIDToUsername("1000")
	assert.Equal(t, "uid:1000", username, "Should return uid as fallback when circuit is open")
	assert.Nil(t, err, "Should not return error, just fallback to uid prefix")

	// Wait for the cooldown to expire
	time.Sleep(1100 * time.Millisecond)

	// Now it should work again
	username, err := proc.resolveUIDToUsername("1000")
	assert.NoError(t, err)
	assert.Equal(t, "testuser", username)
}

// TestCircuitBreakerInExecutablePathFetch tests the integration of circuit breakers with the
// executable path resolution function.
func TestCircuitBreakerInExecutablePathFetch(t *testing.T) {
	// Create processor with circuit breaker enabled and low threshold
	logger := zaptest.NewLogger(t)
	cfg := &Config{
		Enabled:                     true,
		AttributesToFetch:           []string{"executable_path"},
		CircuitBreakerEnabled:       true,
		CircuitBreakerThresholdMs:   5, // Very low threshold to trigger circuit breaker easily
		CircuitBreakerCooldownSeconds: 1, // Short cooldown for faster testing
	}

	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	require.NoError(t, err)

	// Mock the fetchExecutablePath function
	originalFetchExecutablePath := proc.fetchExecutablePath
	defer func() {
		proc.fetchExecutablePath = originalFetchExecutablePath
	}()

	var slowCount int
	proc.fetchExecutablePath = func(pid int) (string, error) {
		if pid == 1000 {
			// Slow operation
			if slowCount < 3 {
				slowCount++
				time.Sleep(10 * time.Millisecond) // Exceed the threshold
				return "/usr/bin/testapp", nil
			}
			// After 3 slow operations, return faster
			return "/usr/bin/testapp", nil
		} else if pid == 1001 {
			return "", errors.New("executable not found")
		}
		
		return "", fmt.Errorf("unknown pid %d", pid)
	}

	// First three attempts should work but be slow
	for i := 0; i < 3; i++ {
		path, err := proc.fetchExecutablePath(1000)
		assert.NoError(t, err)
		assert.Equal(t, "/usr/bin/testapp", path)
	}

	// Now the circuit breaker should be open
	cbObj, _ := proc.circuitBreakers.Load("executable_path")
	cb := cbObj.(*circuitBreaker)
	
	cb.Lock.RLock()
	state := cb.State.State
	cb.Lock.RUnlock()
	
	assert.Equal(t, circuitStateOpen, state, "Circuit should be open after 3 consecutive slow operations")

	// Subsequent attempt should be blocked by circuit breaker
	path, err := proc.fetchExecutablePath(1000)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "operation blocked by circuit breaker")
	assert.Empty(t, path)

	// Wait for the cooldown to expire
	time.Sleep(1100 * time.Millisecond)

	// Now it should work again
	path, err := proc.fetchExecutablePath(1000)
	assert.NoError(t, err)
	assert.Equal(t, "/usr/bin/testapp", path)
}

// TestEnrichProcessAttributesWithCircuitBreaker tests the enrichment function with circuit breakers
func TestEnrichProcessAttributesWithCircuitBreaker(t *testing.T) {
	// Create processor with circuit breaker enabled and low threshold
	logger := zaptest.NewLogger(t)
	cfg := &Config{
		Enabled:                     true,
		AttributesToFetch:           []string{"command_line", "owner", "executable_path"},
		CircuitBreakerEnabled:       true,
		CircuitBreakerThresholdMs:   5, // Very low threshold to trigger circuit breaker easily
		CircuitBreakerCooldownSeconds: 1, // Short cooldown for faster testing
	}

	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	require.NoError(t, err)

	// Mock all the fetch functions
	originalFetchCommandLine := proc.fetchCommandLine
	originalResolveUid := proc.resolveUIDToUsername
	originalFetchExecutablePath := proc.fetchExecutablePath
	
	defer func() {
		proc.fetchCommandLine = originalFetchCommandLine
		proc.resolveUIDToUsername = originalResolveUid
		proc.fetchExecutablePath = originalFetchExecutablePath
	}()

	// Set up slow operations for all fetch methods
	cmdlineSlowCount := 0
	proc.fetchCommandLine = func(pid int) (string, error) {
		if pid == 1000 {
			// Trip the circuit breaker after a few slow calls
			if cmdlineSlowCount < 3 {
				cmdlineSlowCount++
				time.Sleep(10 * time.Millisecond)
				return "test command", nil
			}
			return "test command", nil
		}
		return "", errors.New("command line not found")
	}

	// Initialize circuit breakers to open for username and executable_path
	// This simulates that these operations have already tripped their circuit breakers
	cbUsernameObj, _ := proc.circuitBreakers.Load("username")
	cbUsername := cbUsernameObj.(*circuitBreaker)
	cbUsername.Lock.Lock()
	cbUsername.State.State = circuitStateOpen
	cbUsername.State.UntilTime = time.Now().Add(10 * time.Second)
	cbUsername.Lock.Unlock()

	cbExecPathObj, _ := proc.circuitBreakers.Load("executable_path")
	cbExecPath := cbExecPathObj.(*circuitBreaker)
	cbExecPath.Lock.Lock()
	cbExecPath.State.State = circuitStateOpen
	cbExecPath.State.UntilTime = time.Now().Add(10 * time.Second)
	cbExecPath.Lock.Unlock()

	// Create test attributes with missing fields
	attrs := pcommon.NewMap()
	attrs.PutStr(attrProcessPID, "1000")
	
	// Define the missing attributes we want to enrich
	missingAttrs := []string{
		attrProcessCommandLine,
		attrProcessOwner,
		attrProcessExecutablePath,
	}

	// First enrichment call should only succeed with command line
	// since the other circuit breakers are open
	enriched := proc.enrichProcessAttributes("1000", attrs, missingAttrs)
	assert.True(t, enriched, "Should be partially enriched")
	
	// Verify commandLine was added but not the others
	cmdLine, exists := attrs.Get(attrProcessCommandLine)
	assert.True(t, exists, "Command line should be enriched")
	assert.Equal(t, "test command", cmdLine.Str())
	
	_, exists = attrs.Get(attrProcessOwner)
	assert.False(t, exists, "Owner should not be enriched due to open circuit")
	
	_, exists = attrs.Get(attrProcessExecutablePath)
	assert.False(t, exists, "Executable path should not be enriched due to open circuit")

	// Make several more calls to trip the command line circuit breaker
	for i := 0; i < 3; i++ {
		// Reset attributes
		attrs = pcommon.NewMap()
		attrs.PutStr(attrProcessPID, "1000")
		
		proc.enrichProcessAttributes("1000", attrs, missingAttrs)
	}

	// Now the command line circuit breaker should also be open
	cbCmdlineObj, _ := proc.circuitBreakers.Load("cmdline")
	cbCmdline := cbCmdlineObj.(*circuitBreaker)
	
	cbCmdline.Lock.RLock()
	state := cbCmdline.State.State
	cbCmdline.Lock.RUnlock()
	
	assert.Equal(t, circuitStateOpen, state, "Command line circuit should be open")

	// A final enrichment call should not add any attributes
	attrs = pcommon.NewMap()
	attrs.PutStr(attrProcessPID, "1000")
	
	enriched = proc.enrichProcessAttributes("1000", attrs, missingAttrs)
	assert.False(t, enriched, "Should not be enriched when all circuits are open")
	
	// No attributes should be added
	assert.Equal(t, 1, attrs.Len(), "Only the PID attribute should be present")
}