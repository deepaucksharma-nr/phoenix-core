package fallbackprocparser

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/processor/processortest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

func TestNewProcessor(t *testing.T) {
	factory := NewFactory()
	cfg := factory.CreateDefaultConfig().(*Config)
	
	ctx := context.Background()
	proc, err := factory.CreateMetricsProcessor(ctx, processortest.NewNopCreateSettings(), cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, proc)
}

func TestCheckCriticalAttributes(t *testing.T) {
	cfg := &Config{
		Enabled:            true,
		AttributesToFetch:  []string{"command_line", "owner"},
		StrictMode:         false,
		CriticalAttributes: []string{attrProcessExecutableName, attrProcessCommandLine},
	}
	
	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	require.NoError(t, err)
	
	// Create test attributes
	testCases := []struct {
		name             string
		attributes       map[string]interface{}
		expectedMissing  []string
	}{
		{
			name: "No missing attributes",
			attributes: map[string]interface{}{
				attrProcessExecutableName: "test",
				attrProcessCommandLine:    "./test --arg",
				attrProcessPID:            "123",
			},
			expectedMissing: []string{},
		},
		{
			name: "Missing executable name",
			attributes: map[string]interface{}{
				attrProcessCommandLine: "./test --arg",
				attrProcessPID:         "123",
			},
			expectedMissing: []string{attrProcessExecutableName},
		},
		{
			name: "Missing command line",
			attributes: map[string]interface{}{
				attrProcessExecutableName: "test",
				attrProcessPID:            "123",
			},
			expectedMissing: []string{attrProcessCommandLine},
		},
		{
			name: "Missing both critical attributes",
			attributes: map[string]interface{}{
				attrProcessPID: "123",
			},
			expectedMissing: []string{attrProcessExecutableName, attrProcessCommandLine},
		},
	}
	
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			attrs := pcommon.NewMap()
			for k, v := range tc.attributes {
				if s, ok := v.(string); ok {
					attrs.PutStr(k, s)
				}
			}
			
			missing := proc.checkCriticalAttributes(attrs)
			assert.ElementsMatch(t, tc.expectedMissing, missing)
		})
	}
}

func TestProcessMetricsWithStrictMode(t *testing.T) {
	// Test with strict mode enabled
	cfg := &Config{
		Enabled:            true,
		AttributesToFetch:  []string{"command_line", "owner"},
		StrictMode:         true,
		CriticalAttributes: []string{attrProcessExecutableName, attrProcessCommandLine},
	}
	
	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	require.NoError(t, err)
	
	// Mock the enrichment methods to avoid actual /proc access
	originalEnrichProcessAttributes := proc.enrichProcessAttributes
	defer func() {
		proc.enrichProcessAttributes = originalEnrichProcessAttributes
	}()
	
	proc.enrichProcessAttributes = func(pid string, attrs pcommon.Map, missingAttrs []string) bool {
		// Simulate successful enrichment for PID 123, failed for 456
		if pid == "123" {
			attrs.PutStr(attrProcessCommandLine, "./test --arg")
			return true
		}
		return false
	}
	
	// Create test metrics with different PIDs and attribute combinations
	metrics := createTestMetrics()
	
	// Process metrics
	ctx := context.Background()
	result, err := proc.processMetrics(ctx, metrics)
	require.NoError(t, err)
	
	// Verify results - resource with PID 456 should be filtered out
	resourceMetrics := result.ResourceMetrics()
	assert.Equal(t, 2, resourceMetrics.Len(), "Expected 2 resources (complete and enriched), got %d", resourceMetrics.Len())
}

func TestProcessorShutdown(t *testing.T) {
	ctx := context.Background()
	cfg := &Config{Enabled: true}
	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	require.NoError(t, err)
	
	err = proc.Shutdown(ctx)
	require.NoError(t, err)
}

func TestProcessorStart(t *testing.T) {
	ctx := context.Background()
	cfg := &Config{Enabled: true}
	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	require.NoError(t, err)

	err = proc.Start(ctx, componenttest.NewNopHost())
	require.NoError(t, err)
}

// TestPerformanceMetrics tests the performance metrics functions
func TestPerformanceMetrics(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockRegistry := newMockMetricsRegistry(logger)

	cfg := &Config{
		Enabled:           true,
		AttributesToFetch: []string{"executable_path", "command_line", "owner"},
		CacheTTLSeconds:   60,
	}

	proc := &fallbackProcParserProcessor{
		logger:              logger,
		config:              cfg,
		metricsRegistry:     mockRegistry,
		cacheTTL:            time.Duration(cfg.CacheTTLSeconds) * time.Second,
		executablePathCache: make(map[string]interface{}),
		commandLineCache:    make(map[string]interface{}),
		usernameCache:       make(map[string]interface{}),
	}

	// Test recordAttributeResolution function
	t.Run("Test recordAttributeResolution", func(t *testing.T) {
		// Record cache hit for username attribute
		proc.recordAttributeResolution("username", true)

		// Record cache miss (proc lookup) for executable_path attribute
		proc.recordAttributeResolution("executable_path", false)

		// Verify metric values and labels
		counterValue := mockRegistry.GetCounterValue("pte_fallback_parser_attribute_resolution_total")
		assert.Equal(t, 2.0, counterValue, "Counter should be incremented twice")

		// Check that the last call's labels are stored (this is a limitation of our mock)
		labels := mockRegistry.GetCounterLabels("pte_fallback_parser_attribute_resolution_total")
		assert.Equal(t, "executable_path", labels["attribute_type"], "Attribute type label should be set")
		assert.Equal(t, "proc", labels["source"], "Source label should be set for cache miss")
	})

	// Test recordFetchDuration function
	t.Run("Test recordFetchDuration", func(t *testing.T) {
		// Record durations for different attribute types
		proc.recordFetchDuration("username", 1.5)
		proc.recordFetchDuration("executable_path", 2.5)
		proc.recordFetchDuration("cmdline", 0.8)

		// Verify histogram values
		histValues := mockRegistry.GetHistogramValues("pte_fallback_parser_fetch_duration_ms")
		assert.Equal(t, 3, len(histValues), "Should have 3 histogram entries")
		assert.Contains(t, histValues, 1.5, "Histogram should contain username duration")
		assert.Contains(t, histValues, 2.5, "Histogram should contain executable_path duration")
		assert.Contains(t, histValues, 0.8, "Histogram should contain cmdline duration")

		// Check the last label set
		labels := mockRegistry.GetHistogramLabels("pte_fallback_parser_fetch_duration_ms")
		assert.Equal(t, "cmdline", labels["attribute_type"], "Attribute type label should be set")
	})

	// Test cache hit/miss recording
	t.Run("Test cache hit/miss metrics", func(t *testing.T) {
		// Record cache hits
		proc.recordCacheHit("username")
		proc.recordCacheHit("executable_path")

		// Record cache misses
		proc.recordCacheMiss("cmdline")

		// Verify counter values
		hitValue := mockRegistry.GetCounterValue("pte_fallback_parser_cache_hit_total")
		missValue := mockRegistry.GetCounterValue("pte_fallback_parser_cache_miss_total")

		assert.Equal(t, 2.0, hitValue, "Hit counter should be incremented twice")
		assert.Equal(t, 1.0, missValue, "Miss counter should be incremented once")

		// Check labels
		hitLabels := mockRegistry.GetCounterLabels("pte_fallback_parser_cache_hit_total")
		missLabels := mockRegistry.GetCounterLabels("pte_fallback_parser_cache_miss_total")

		assert.Equal(t, "executable_path", hitLabels["cache_type"], "Cache type label for hit should be set")
		assert.Equal(t, "cmdline", missLabels["cache_type"], "Cache type label for miss should be set")
	})

	// Test error recording
	t.Run("Test error metrics", func(t *testing.T) {
		// Record errors
		testErr := fmt.Errorf("permission denied: /proc/123/exe")
		proc.recordErrorMetric("exe_readlink", testErr)

		testErr2 := fmt.Errorf("no such file or directory")
		proc.recordErrorMetric("uid_lookup", testErr2)

		// Verify counter values
		errorValue := mockRegistry.GetCounterValue("pte_fallback_parser_fetch_error_total")
		detailedErrorValue := mockRegistry.GetCounterValue("pte_fallback_parser_error_by_type_total")

		assert.Equal(t, 2.0, errorValue, "Error counter should be incremented twice")
		assert.Equal(t, 2.0, detailedErrorValue, "Detailed error counter should be incremented twice")

		// Check labels for the last error
		errorLabels := mockRegistry.GetCounterLabels("pte_fallback_parser_fetch_error_total")
		detailedLabels := mockRegistry.GetCounterLabels("pte_fallback_parser_error_by_type_total")

		assert.Equal(t, "uid_lookup", errorLabels["error_type"], "Error type label should be set")
		assert.Equal(t, "uid_lookup", detailedLabels["error_type"], "Error type label should be set in detailed metrics")
		assert.Equal(t, "no such file or directory", detailedLabels["error_message"], "Error message should be extracted")
	})
}

// TestFetchExecutablePath tests the executable path resolution functionality
func TestFetchExecutablePath(t *testing.T) {
	// Create a mock proc filesystem
	mockProc := newMockProcFS(t)
	defer mockProc.Cleanup()

	// Add some test processes
	mockProc.AddProcess(1001, WithExecutablePath("/usr/bin/test-process"))
	mockProc.AddProcess(1002, WithExecutablePath("/usr/local/bin/another-process"))
	mockProc.AddProcess(1003, WithAccessErrors()) // Will cause errors when accessed
	mockProc.AddProcess(1004, WithNonExistent())  // Doesn't actually exist

	// Create a processor with a mocked filesystem root
	logger := zaptest.NewLogger(t)
	cfg := &Config{
		Enabled:           true,
		AttributesToFetch: []string{"executable_path", "command_line", "owner"},
		CacheTTLSeconds:   60,
	}

	proc := &fallbackProcParserProcessor{
		logger:              logger,
		config:              cfg,
		metricsRegistry:     nil, // We'll mock metrics calls
		cacheTTL:            time.Duration(cfg.CacheTTLSeconds) * time.Second,
		executablePathCache: make(map[string]interface{}),
	}

	// Override the os.Readlink function to use our mock
	originalReadlink := osReadlink
	defer func() { osReadlink = originalReadlink }()

	osReadlink = func(name string) (string, error) {
		// Extract the PID from the path
		pid := 0
		fmt.Sscanf(name, mockProc.GetRoot()+"/%d/exe", &pid)

		// Check if this process exists and has proper permissions
		if mockProc.processes[pid] == nil || !mockProc.processes[pid].shouldExist {
			return "", fmt.Errorf("process does not exist")
		}
		if mockProc.processes[pid].accessErrors {
			return "", fmt.Errorf("permission denied")
		}

		return mockProc.processes[pid].execPath, nil
	}

	// Test successful executable path resolution
	execPath, err := proc.fetchExecutablePath(1001)
	assert.NoError(t, err)
	assert.Equal(t, "/usr/bin/test-process", execPath)

	// Test another successful path (with different path)
	execPath, err = proc.fetchExecutablePath(1002)
	assert.NoError(t, err)
	assert.Equal(t, "/usr/local/bin/another-process", execPath)

	// Test permission errors
	execPath, err = proc.fetchExecutablePath(1003)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read executable path")
	assert.Empty(t, execPath)

	// Test non-existent process
	execPath, err = proc.fetchExecutablePath(1004)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read executable path")
	assert.Empty(t, execPath)

	// Test caching behavior - second call for same PID should use cache
	// Call the already successfully resolved PID again
	execPath, err = proc.fetchExecutablePath(1001)
	assert.NoError(t, err)
	assert.Equal(t, "/usr/bin/test-process", execPath)

	// Test cached error case
	execPath, err = proc.fetchExecutablePath(1003)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read executable path")
}

// TestExecutablePathEnrichment tests the enrichment of executable path attribute
func TestExecutablePathEnrichment(t *testing.T) {
	// Create a mock proc filesystem
	mockProc := newMockProcFS(t)
	defer mockProc.Cleanup()

	// Add test processes with various attributes
	mockProc.AddProcess(2001, WithExecutablePath("/usr/bin/test-app"))
	mockProc.AddProcess(2002, WithExecutablePath("/usr/local/bin/app-server"))
	mockProc.AddProcess(2003, WithAccessErrors())

	// Create a processor with a mocked filesystem root
	logger := zaptest.NewLogger(t)
	cfg := &Config{
		Enabled:            true,
		AttributesToFetch:  []string{"executable_path"},
		StrictMode:         false,
		CriticalAttributes: []string{attrProcessExecutableName, attrProcessCommandLine},
		CacheTTLSeconds:    60,
	}

	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	require.NoError(t, err)

	// Mock the executable path function
	originalFetchExecutablePath := proc.fetchExecutablePath
	defer func() {
		proc.fetchExecutablePath = originalFetchExecutablePath
	}()

	proc.fetchExecutablePath = func(pid int) (string, error) {
		switch pid {
		case 2001:
			return "/usr/bin/test-app", nil
		case 2002:
			return "/usr/local/bin/app-server", nil
		default:
			return "", fmt.Errorf("failed to read executable path for PID %d", pid)
		}
	}

	// Create test cases
	testCases := []struct {
		name           string
		pid            string
		initialAttrs   map[string]interface{}
		expectedAttrs  map[string]interface{}
		shouldBeEnriched bool
	}{
		{
			name: "Add executable path",
			pid:  "2001",
			initialAttrs: map[string]interface{}{
				attrProcessPID:             "2001",
				attrProcessExecutableName:  "test-app",
			},
			expectedAttrs: map[string]interface{}{
				attrProcessPID:             "2001",
				attrProcessExecutableName:  "test-app",
				attrProcessExecutablePath:  "/usr/bin/test-app",
			},
			shouldBeEnriched: true,
		},
		{
			name: "Add executable path and derive executable name",
			pid:  "2002",
			initialAttrs: map[string]interface{}{
				attrProcessPID: "2002",
				// Deliberately missing executable name
			},
			expectedAttrs: map[string]interface{}{
				attrProcessPID:             "2002",
				attrProcessExecutableName:  "app-server", // Derived from path
				attrProcessExecutablePath:  "/usr/local/bin/app-server",
			},
			shouldBeEnriched: true,
		},
		{
			name: "Executable path fetch fails",
			pid:  "2003",
			initialAttrs: map[string]interface{}{
				attrProcessPID:             "2003",
				attrProcessExecutableName:  "unknown-app",
			},
			expectedAttrs: map[string]interface{}{
				attrProcessPID:             "2003",
				attrProcessExecutableName:  "unknown-app",
				// No executable path added
			},
			shouldBeEnriched: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create resource attributes
			attrs := pcommon.NewMap()
			for k, v := range tc.initialAttrs {
				if s, ok := v.(string); ok {
					attrs.PutStr(k, s)
				}
			}

			// This is the attribute we want to test
			missingAttrs := []string{attrProcessExecutablePath}

			// Call enrichProcessAttributes
			enriched := proc.enrichProcessAttributes(tc.pid, attrs, missingAttrs)

			// Check if enrichment succeeded as expected
			assert.Equal(t, tc.shouldBeEnriched, enriched)

			// Verify all expected attributes are present
			for k, v := range tc.expectedAttrs {
				if s, ok := v.(string); ok {
					val, exists := attrs.Get(k)
					assert.True(t, exists, "Attribute %s should exist", k)
					if exists {
						assert.Equal(t, s, val.Str(), "Attribute %s value mismatch", k)
					}
				}
			}
		})
	}
}

// TestProcessMetricsWithExecutablePath tests the full processing of metrics with executable path
func TestProcessMetricsWithExecutablePath(t *testing.T) {
	// Create a processor with executable_path in attributesToFetch
	cfg := &Config{
		Enabled:            true,
		AttributesToFetch:  []string{"command_line", "owner", "executable_path"},
		StrictMode:         false,
		CriticalAttributes: []string{attrProcessExecutableName, attrProcessCommandLine},
		CacheTTLSeconds:    60,
	}

	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	require.NoError(t, err)

	// Mock the enrichment method to avoid actual /proc access
	originalEnrichProcessAttributes := proc.enrichProcessAttributes
	defer func() {
		proc.enrichProcessAttributes = originalEnrichProcessAttributes
	}()

	proc.enrichProcessAttributes = func(pid string, attrs pcommon.Map, missingAttrs []string) bool {
		// For testing executable path enrichment
		if pid == "789" {
			attrs.PutStr(attrProcessExecutablePath, "/usr/bin/test-app")
			// Also set the executable name if it's missing
			if _, exists := attrs.Get(attrProcessExecutableName); !exists {
				attrs.PutStr(attrProcessExecutableName, "test-app")
			}
			return true
		}
		// For testing command line enrichment
		if pid == "123" {
			attrs.PutStr(attrProcessCommandLine, "./test --arg")
			return true
		}
		return false
	}

	// Create test metrics with different PIDs and attribute combinations
	metrics := createTestMetricsWithExecutablePath()

	// Process metrics
	ctx := context.Background()
	result, err := proc.processMetrics(ctx, metrics)
	require.NoError(t, err)

	// Verify results - all resources should be present, with enrichments where possible
	resourceMetrics := result.ResourceMetrics()
	assert.Equal(t, 4, resourceMetrics.Len(), "Expected 4 resources")

	// Check the resource that had executable path added (PID 789)
	foundEnriched := false
	for i := 0; i < resourceMetrics.Len(); i++ {
		rm := resourceMetrics.At(i)
		pid, ok := rm.Resource().Attributes().Get(attrProcessPID)
		if ok && pid.Str() == "789" {
			exePath, ok := rm.Resource().Attributes().Get(attrProcessExecutablePath)
			assert.True(t, ok, "Expected executable path to be added")
			assert.Equal(t, "/usr/bin/test-app", exePath.Str())

			execName, ok := rm.Resource().Attributes().Get(attrProcessExecutableName)
			assert.True(t, ok, "Expected executable name to be set")
			assert.Equal(t, "test-app", execName.Str())

			foundEnriched = true
			break
		}
	}
	assert.True(t, foundEnriched, "Expected to find resource with PID 789 and enriched executable path")
}

// Helper function to create test metrics
func createTestMetrics() pmetric.Metrics {
	md := pmetric.NewMetrics()

	// Resource with all attributes
	rm1 := md.ResourceMetrics().AppendEmpty()
	rm1.Resource().Attributes().PutStr(attrProcessPID, "100")
	rm1.Resource().Attributes().PutStr(attrProcessExecutableName, "complete")
	rm1.Resource().Attributes().PutStr(attrProcessCommandLine, "./complete --arg")

	// Resource with missing command line that can be enriched
	rm2 := md.ResourceMetrics().AppendEmpty()
	rm2.Resource().Attributes().PutStr(attrProcessPID, "123")
	rm2.Resource().Attributes().PutStr(attrProcessExecutableName, "enrichable")

	// Resource with multiple missing attributes that cannot be enriched
	rm3 := md.ResourceMetrics().AppendEmpty()
	rm3.Resource().Attributes().PutStr(attrProcessPID, "456")

	return md
}

// Helper function to create test metrics with a focus on executable path
func createTestMetricsWithExecutablePath() pmetric.Metrics {
	md := pmetric.NewMetrics()

	// Resource with all attributes including executable path
	rm1 := md.ResourceMetrics().AppendEmpty()
	rm1.Resource().Attributes().PutStr(attrProcessPID, "100")
	rm1.Resource().Attributes().PutStr(attrProcessExecutableName, "complete")
	rm1.Resource().Attributes().PutStr(attrProcessCommandLine, "./complete --arg")
	rm1.Resource().Attributes().PutStr(attrProcessExecutablePath, "/usr/bin/complete")

	// Resource with missing command line that can be enriched
	rm2 := md.ResourceMetrics().AppendEmpty()
	rm2.Resource().Attributes().PutStr(attrProcessPID, "123")
	rm2.Resource().Attributes().PutStr(attrProcessExecutableName, "enrichable")

	// Resource with missing executable path that can be enriched
	rm3 := md.ResourceMetrics().AppendEmpty()
	rm3.Resource().Attributes().PutStr(attrProcessPID, "789")
	rm3.Resource().Attributes().PutStr(attrProcessCommandLine, "./app --serve")

	// Resource with multiple missing attributes that cannot be enriched
	rm4 := md.ResourceMetrics().AppendEmpty()
	rm4.Resource().Attributes().PutStr(attrProcessPID, "456")

	return md
}