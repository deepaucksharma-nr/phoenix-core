package fallbackprocparser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/deepaucksharma-nr/phoenix-core/internal/metrics"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/processor/processortest"
	"go.uber.org/zap/zaptest"
)

// Benchmark for executable path resolution
func BenchmarkExecutablePathResolution(b *testing.B) {
	// Setup mock filesystem with test processes
	mockProc := newMockProcFS(b)
	defer mockProc.Cleanup()

	// Add test processes
	for i := 1; i <= 1000; i++ {
		mockProc.AddProcess(i, WithExecutablePath(fmt.Sprintf("/usr/bin/process-%d", i)))
	}

	logger := zaptest.NewLogger(b)
	cfg := &Config{
		Enabled:           true,
		AttributesToFetch: []string{"executable_path"},
		CacheTTLSeconds:   300,
	}

	// Create processor
	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	if err != nil {
		b.Fatalf("Failed to create processor: %v", err)
	}

	// Mock the osReadlink function
	originalReadlink := osReadlink
	defer func() { osReadlink = originalReadlink }()

	osReadlink = func(name string) (string, error) {
		var pid int
		fmt.Sscanf(name, "/proc/%d/exe", &pid)
		if pid > 0 && pid <= 1000 {
			return fmt.Sprintf("/usr/bin/process-%d", pid), nil
		}
		return "", fmt.Errorf("process not found")
	}

	// Reset timer before the benchmark loop
	b.ResetTimer()

	// Benchmark executable path resolution
	for i := 0; i < b.N; i++ {
		// Use different PIDs in round-robin fashion
		pid := (i % 1000) + 1
		_, _ = proc.fetchExecutablePath(pid)
	}
}

// Benchmark for executable path resolution with cache
func BenchmarkExecutablePathResolutionWithCache(b *testing.B) {
	// Setup processor with cache
	logger := zaptest.NewLogger(b)
	cfg := &Config{
		Enabled:           true,
		AttributesToFetch: []string{"executable_path"},
		CacheTTLSeconds:   300,
	}

	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	if err != nil {
		b.Fatalf("Failed to create processor: %v", err)
	}

	// Mock the osReadlink function
	originalReadlink := osReadlink
	defer func() { osReadlink = originalReadlink }()

	osReadlink = func(name string) (string, error) {
		var pid int
		fmt.Sscanf(name, "/proc/%d/exe", &pid)
		// Simulate a slow filesystem lookup (1ms)
		time.Sleep(time.Millisecond)
		if pid > 0 && pid <= 100 {
			return fmt.Sprintf("/usr/bin/process-%d", pid), nil
		}
		return "", fmt.Errorf("process not found")
	}

	// Warm up the cache with 100 PIDs
	for i := 1; i <= 100; i++ {
		_, _ = proc.fetchExecutablePath(i)
	}

	// Reset timer before the benchmark loop
	b.ResetTimer()

	// Benchmark with 90% cache hits (reuse PIDs 1-90 repeatedly, 91-100 force cache misses)
	for i := 0; i < b.N; i++ {
		pid := (i % 100) + 1
		_, _ = proc.fetchExecutablePath(pid)
	}
}

// Benchmark username resolution
func BenchmarkUsernameResolution(b *testing.B) {
	// Create processor with username resolution
	logger := zaptest.NewLogger(b)
	cfg := &Config{
		Enabled:           true,
		AttributesToFetch: []string{"owner"},
		CacheTTLSeconds:   300,
	}

	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	if err != nil {
		b.Fatalf("Failed to create processor: %v", err)
	}

	// We'll need to mock the resolveUIDToUsername function
	// to avoid actual user lookups during benchmarking
	originalResolveUID := proc.resolveUIDToUsername
	defer func() {
		// Restore original function after benchmark
		if proc, ok := proc.(*fallbackProcParserProcessor); ok {
			proc.resolveUIDToUsername = originalResolveUID
		}
	}()

	// Replace with mocked version
	if procTyped, ok := proc.(*fallbackProcParserProcessor); ok {
		procTyped.resolveUIDToUsername = func(uid string) (string, error) {
			// Simulate a slow system call (5ms)
			time.Sleep(5 * time.Millisecond)
			
			// Generate a predictable username
			uidInt, err := strconv.Atoi(uid)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("user-%d", uidInt), nil
		}
	}

	// Reset timer before the benchmark
	b.ResetTimer()

	// Benchmark username resolution with 10 different UIDs
	for i := 0; i < b.N; i++ {
		uid := strconv.Itoa((i % 10) + 1000)
		if procTyped, ok := proc.(*fallbackProcParserProcessor); ok {
			_, _ = procTyped.resolveUIDToUsername(uid)
		}
	}
}

// Benchmark owner field lookup from /proc/[pid]/status with cache
func BenchmarkOwnerResolutionWithCache(b *testing.B) {
	// Create processor with owner resolution
	logger := zaptest.NewLogger(b)
	cfg := &Config{
		Enabled:           true,
		AttributesToFetch: []string{"owner"},
		CacheTTLSeconds:   300,
	}

	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	if err != nil {
		b.Fatalf("Failed to create processor: %v", err)
	}

	// We'll need to mock the fetchOwner function to avoid actual proc access
	originalFetchOwner := proc.fetchOwner
	defer func() {
		// Restore original function after benchmark
		if procTyped, ok := proc.(*fallbackProcParserProcessor); ok {
			procTyped.fetchOwner = originalFetchOwner
		}
	}()

	// Replace with mocked version
	if procTyped, ok := proc.(*fallbackProcParserProcessor); ok {
		procTyped.fetchOwner = func(pid int) (string, error) {
			// Simulate filesystem access + username resolution time (6ms total)
			time.Sleep(6 * time.Millisecond)
			return fmt.Sprintf("user-%d", pid), nil
		}
	}

	// Warm up the cache with 100 PIDs
	for i := 1; i <= 100; i++ {
		if procTyped, ok := proc.(*fallbackProcParserProcessor); ok {
			_, _ = procTyped.fetchOwner(i)
		}
	}

	// Reset timer before the benchmark
	b.ResetTimer()

	// Benchmark with 90% cache hits (reuse PIDs 1-90 repeatedly, 91-100 force cache misses)
	for i := 0; i < b.N; i++ {
		pid := (i % 100) + 1
		if procTyped, ok := proc.(*fallbackProcParserProcessor); ok {
			_, _ = procTyped.fetchOwner(pid)
		}
	}
}

// Create a temporary directory with realistic /proc structure for benchmarking
func createTestProcFS(b *testing.B, numProcesses int) string {
	tempDir, err := os.MkdirTemp("", "benchmark-procfs")
	if err != nil {
		b.Fatalf("Failed to create temp directory: %v", err)
	}

	// Create process directories with common files
	for i := 1; i <= numProcesses; i++ {
		pid := i
		procDir := filepath.Join(tempDir, strconv.Itoa(pid))
		err := os.MkdirAll(procDir, 0755)
		if err != nil {
			b.Fatalf("Failed to create process directory: %v", err)
		}

		// Create cmdline file
		cmdlineContent := fmt.Sprintf("process-%d\x00--arg1\x00--arg2", pid)
		err = os.WriteFile(filepath.Join(procDir, "cmdline"), []byte(cmdlineContent), 0644)
		if err != nil {
			b.Fatalf("Failed to create cmdline file: %v", err)
		}

		// Create status file
		statusContent := fmt.Sprintf("Name:\tprocess-%d\nState:\tS (sleeping)\nPid:\t%d\nUid:\t%d\t%d\t%d\t%d\n",
			pid, pid, 1000+pid, 1000+pid, 1000+pid, 1000+pid)
		err = os.WriteFile(filepath.Join(procDir, "status"), []byte(statusContent), 0644)
		if err != nil {
			b.Fatalf("Failed to create status file: %v", err)
		}

		// Create exe symlink target
		exeTarget := filepath.Join(tempDir, fmt.Sprintf("bin-process-%d", pid))
		err = os.WriteFile(exeTarget, []byte("#!/bin/sh\necho 'mock executable'\n"), 0755)
		if err != nil {
			b.Fatalf("Failed to create exe target: %v", err)
		}

		// Create exe symlink
		err = os.Symlink(fmt.Sprintf("/usr/bin/process-%d", pid), filepath.Join(procDir, "exe"))
		if err != nil {
			b.Fatalf("Failed to create exe symlink: %v", err)
		}
	}

	return tempDir
}

// Benchmark full process metrics enrichment
func BenchmarkProcessMetricsEnrichment(b *testing.B) {
	// Create processor with all attribute fetching enabled
	logger := zaptest.NewLogger(b)
	cfg := &Config{
		Enabled:            true,
		AttributesToFetch:  []string{"command_line", "owner", "executable_path"},
		StrictMode:         false,
		CriticalAttributes: []string{"process.executable.name", "process.command_line"},
		CacheTTLSeconds:    300,
	}

	proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
	if err != nil {
		b.Fatalf("Failed to create processor: %v", err)
	}

	// We need to mock all the attribute fetching functions
	// for benchmarking without actual filesystem access
	if procTyped, ok := proc.(*fallbackProcParserProcessor); ok {
		// Mock executable path fetching
		originalExecPath := procTyped.fetchExecutablePath
		defer func() { procTyped.fetchExecutablePath = originalExecPath }()
		procTyped.fetchExecutablePath = func(pid int) (string, error) {
			time.Sleep(time.Millisecond) // Simulate filesystem access
			return fmt.Sprintf("/usr/bin/process-%d", pid), nil
		}

		// Mock command line fetching
		originalCmdLine := procTyped.fetchCommandLine
		defer func() { procTyped.fetchCommandLine = originalCmdLine }()
		procTyped.fetchCommandLine = func(pid int) (string, error) {
			time.Sleep(time.Millisecond) // Simulate filesystem access
			return fmt.Sprintf("process-%d --arg1 --arg2", pid), nil
		}

		// Mock owner fetching
		originalOwner := procTyped.fetchOwner
		defer func() { procTyped.fetchOwner = originalOwner }()
		procTyped.fetchOwner = func(pid int) (string, error) {
			time.Sleep(2 * time.Millisecond) // Simulate UID lookup + resolution
			return fmt.Sprintf("user-%d", pid), nil
		}
	}

	// Create test metrics
	numProcesses := 100
	metrics := createBenchmarkMetrics(numProcesses)

	// Reset timer before the benchmark
	b.ResetTimer()

	// Process metrics repeatedly
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		_, err := proc.processMetrics(ctx, metrics)
		if err != nil {
			b.Fatalf("Error processing metrics: %v", err)
		}
	}
}

// Helper function to create test metrics for benchmarking
func createBenchmarkMetrics(numProcesses int) pmetric.Metrics {
	md := pmetric.NewMetrics()

	// Create process resources with varying attributes
	for i := 1; i <= numProcesses; i++ {
		rm := md.ResourceMetrics().AppendEmpty()
		attrs := rm.Resource().Attributes()

		// Always set PID
		attrs.PutStr(attrProcessPID, strconv.Itoa(i))

		// Vary which attributes are missing to test different enrichment paths
		switch i % 4 {
		case 0:
			// All attributes present
			attrs.PutStr(attrProcessExecutableName, fmt.Sprintf("process-%d", i))
			attrs.PutStr(attrProcessCommandLine, fmt.Sprintf("process-%d --arg1 --arg2", i))
			attrs.PutStr(attrProcessOwner, fmt.Sprintf("user-%d", i))
			attrs.PutStr(attrProcessExecutablePath, fmt.Sprintf("/usr/bin/process-%d", i))
		case 1:
			// Missing executable path
			attrs.PutStr(attrProcessExecutableName, fmt.Sprintf("process-%d", i))
			attrs.PutStr(attrProcessCommandLine, fmt.Sprintf("process-%d --arg1 --arg2", i))
		case 2:
			// Missing command line
			attrs.PutStr(attrProcessExecutableName, fmt.Sprintf("process-%d", i))
			attrs.PutStr(attrProcessExecutablePath, fmt.Sprintf("/usr/bin/process-%d", i))
		case 3:
			// Missing exec name and command line
			attrs.PutStr(attrProcessOwner, fmt.Sprintf("user-%d", i))
		}

		// Add some metrics
		ms := rm.ScopeMetrics().AppendEmpty().Metrics()
		m := ms.AppendEmpty()
		m.SetName("process.cpu.time")
		m.SetEmptySum()
		dp := m.Sum().DataPoints().AppendEmpty()
		dp.SetDoubleValue(float64(i * 100))
	}

	return md
}

// Benchmark for cache efficiency under load
func BenchmarkCacheEfficiency(b *testing.B) {
	b.Run("CommandLineCaching", func(b *testing.B) {
		// CommandLine caching benchmark
		benchmarkCacheEfficiency(b, "command_line", 5*time.Millisecond)
	})

	b.Run("UsernameCaching", func(b *testing.B) {
		// Username caching benchmark (slower operation)
		benchmarkCacheEfficiency(b, "username", 10*time.Millisecond)
	})

	b.Run("ExecutablePathCaching", func(b *testing.B) {
		// Executable path caching benchmark
		benchmarkCacheEfficiency(b, "executable_path", 3*time.Millisecond)
	})
}

// Helper function for benchmarking cache efficiency with different cache sizes
func benchmarkCacheEfficiency(b *testing.B, cacheType string, operationTime time.Duration) {
	// Test cache efficiency with different working set sizes
	cacheSizes := []int{10, 100, 1000, 10000}
	cacheHitRatios := []float64{0.9, 0.75, 0.5}

	for _, cacheSize := range cacheSizes {
		for _, hitRatio := range cacheHitRatios {
			testName := fmt.Sprintf("Size_%d_HitRatio_%.2f", cacheSize, hitRatio)
			b.Run(testName, func(b *testing.B) {
				// Create processor with appropriate config
				logger := zaptest.NewLogger(b)
				cfg := &Config{
					Enabled:          true,
					CacheTTLSeconds:  300,
					AttributesToFetch: []string{"command_line", "owner", "executable_path"},
				}

				proc, err := newProcessor(processortest.NewNopCreateSettings(), cfg)
				if err != nil {
					b.Fatalf("Failed to create processor: %v", err)
				}

				// Different caching functions for each type
				mockFunc := func(key int) (string, error) {
					time.Sleep(operationTime) // Simulate operation time
					return fmt.Sprintf("value-%d", key), nil
				}

				// Number of unique keys that will be requested during benchmark
				uniqueKeys := cacheSize

				// Number of keys that should get cache hits (cached working set)
				cachedKeys := int(float64(uniqueKeys) * hitRatio)

				// Warm up the cache for the "cached" portion
				for i := 1; i <= cachedKeys; i++ {
					if cacheType == "command_line" {
						if p, ok := proc.(*fallbackProcParserProcessor); ok {
							p.fetchCommandLine = func(pid int) (string, error) {
								// Store result directly in cache to prevent actual calls during warmup
								p.commandLineCache.Store(strconv.Itoa(pid), cacheEntry{
									Value:    fmt.Sprintf("cmdline-%d", pid),
									Expiry:   time.Now().Add(p.cacheTTL),
									HasError: false,
								})
								return fmt.Sprintf("cmdline-%d", pid), nil
							}
						}
					} else if cacheType == "username" {
						if p, ok := proc.(*fallbackProcParserProcessor); ok {
							p.resolveUIDToUsername = func(uid string) (string, error) {
								// Store result directly in cache to prevent actual calls during warmup
								p.usernameCache.Store(uid, cacheEntry{
									Value:    fmt.Sprintf("user-%s", uid),
									Expiry:   time.Now().Add(p.cacheTTL),
									HasError: false,
								})
								return fmt.Sprintf("user-%s", uid), nil
							}
						}
					} else if cacheType == "executable_path" {
						if p, ok := proc.(*fallbackProcParserProcessor); ok {
							p.fetchExecutablePath = func(pid int) (string, error) {
								// Store result directly in cache to prevent actual calls during warmup
								p.executablePathCache.Store(strconv.Itoa(pid), cacheEntry{
									Value:    fmt.Sprintf("/usr/bin/process-%d", pid),
									Expiry:   time.Now().Add(p.cacheTTL),
									HasError: false,
								})
								return fmt.Sprintf("/usr/bin/process-%d", pid), nil
							}
						}
					}
				}

				// Reset mock functions to measure actual cache performance
				if p, ok := proc.(*fallbackProcParserProcessor); ok {
					if cacheType == "command_line" {
						p.fetchCommandLine = func(pid int) (string, error) {
							time.Sleep(operationTime) // Simulate filesystem access
							return fmt.Sprintf("cmdline-%d", pid), nil
						}
					} else if cacheType == "username" {
						p.resolveUIDToUsername = func(uid string) (string, error) {
							time.Sleep(operationTime) // Simulate system call
							return fmt.Sprintf("user-%s", uid), nil
						}
					} else if cacheType == "executable_path" {
						p.fetchExecutablePath = func(pid int) (string, error) {
							time.Sleep(operationTime) // Simulate filesystem access
							return fmt.Sprintf("/usr/bin/process-%d", pid), nil
						}
					}
				}

				// Reset timer before the benchmark
				b.ResetTimer()

				// Run benchmark requests with the specified hit ratio
				for i := 0; i < b.N; i++ {
					// Pick a key that cycles through all unique keys
					key := (i % uniqueKeys) + 1

					// Call the appropriate function
					if p, ok := proc.(*fallbackProcParserProcessor); ok {
						if cacheType == "command_line" {
							_, _ = p.fetchCommandLine(key)
						} else if cacheType == "username" {
							_, _ = p.resolveUIDToUsername(strconv.Itoa(key))
						} else if cacheType == "executable_path" {
							_, _ = p.fetchExecutablePath(key)
						}
					}
				}
			})
		}
	}
}