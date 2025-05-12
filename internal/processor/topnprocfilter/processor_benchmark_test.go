package topnprocfilter

import (
	"testing"
	"time"
)

// benchProcessMetric creates a test processMetric
func benchProcessMetric(pid string, cpu, mem float64) *processMetric {
	now := time.Now().Unix()
	return &processMetric{
		PID:                    pid,
		ProcessName:            "test-process-" + pid,
		CPUUsage:               cpu,
		MemoryUsage:            mem,
		LastUpdatedUnix:        now,
		LastAboveThresholdUnix: now,
	}
}

// BenchmarkTopNSelection benchmarks the top-N selection algorithm
func BenchmarkTopNSelection(b *testing.B) {
	// Test with different input sizes
	sizes := []int{10, 100, 1000, 10000}
	
	for _, size := range sizes {
		b.Run("Size-"+string(rune(size)), func(b *testing.B) {
			// Create test data
			processes := make([]*processMetric, size)
			for i := 0; i < size; i++ {
				processes[i] = benchProcessMetric(
					string(rune(i)),
					float64(i)/float64(size), // CPU between 0 and 1 
					float64(size-i)/float64(size), // Memory between 0 and 1
				)
			}
			
			// Top-N sizes to test
			topNSizes := []int{5, 10, 50, 100}
			for _, n := range topNSizes {
				if n > size {
					continue // Skip if n > size
				}
				
				b.Run("TopN-"+string(rune(n)), func(b *testing.B) {
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						// Run the optimized algorithm
						_ = getTopNByMetricOptimized(processes, n, func(p *processMetric) float64 {
							return p.CPUUsage
						})
					}
				})
			}
		})
	}
}

// BenchmarkQuickSelectVsHeap compares the old QuickSelect algorithm with the new Heap-based algorithm
func BenchmarkQuickSelectVsHeap(b *testing.B) {
	// Create test data - 10,000 processes
	size := 10000
	processes := make([]*processMetric, size)
	for i := 0; i < size; i++ {
		processes[i] = benchProcessMetric(
			string(rune(i)),
			float64(i)/float64(size),        // CPU between 0 and 1
			float64(size-i)/float64(size),   // Memory between 0 and 1
		)
	}
	
	// Create a copy for each algorithm to use
	processesCopy1 := make([]*processMetric, size)
	processesCopy2 := make([]*processMetric, size)
	
	// Function to select value for sorting
	cpuSelector := func(p *processMetric) float64 { return p.CPUUsage }
	
	// Top-N sizes to test
	topNSizes := []int{10, 50, 100, 500}
	
	for _, n := range topNSizes {
		b.Run("N-"+string(rune(n)), func(b *testing.B) {
			// Quick Select (old)
			b.Run("QuickSelect", func(b *testing.B) {
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					// Make a fresh copy for each run to avoid side effects
					copy(processesCopy1, processes)
					quickSelect(processesCopy1, 0, len(processesCopy1)-1, n, cpuSelector)
					_ = processesCopy1[:n]
				}
			})
			
			// Heap (new)
			b.Run("Heap", func(b *testing.B) {
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					// Make a fresh copy for each run
					copy(processesCopy2, processes)
					_ = getTopNByMetricOptimized(processesCopy2, n, cpuSelector)
				}
			})
		})
	}
}

// BenchmarkMemoryUsage benchmarks memory usage of the old vs new process metric struct
// This is just a simple benchmark to demonstrate memory savings, not an actual functional test
func BenchmarkMemoryUsage(b *testing.B) {
	b.Run("NewFormat", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			now := time.Now().Unix()
			_ = &processMetric{
				PID:                    "12345",
				ProcessName:            "test-process",
				CPUUsage:               0.5,
				MemoryUsage:            0.3,
				LastUpdatedUnix:        now,
				LastAboveThresholdUnix: now,
			}
		}
	})
	
	// Create a struct similar to the old version for comparison
	type oldProcessMetric struct {
		PID                string
		ProcessName        string
		CPUUsage           float64
		MemoryUsage        float64
		LastUpdated        time.Time
		LastAboveThreshold time.Time
	}
	
	b.Run("OldFormat", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			now := time.Now()
			_ = &oldProcessMetric{
				PID:                "12345",
				ProcessName:        "test-process",
				CPUUsage:           0.5,
				MemoryUsage:        0.3,
				LastUpdated:        now,
				LastAboveThreshold: now,
			}
		}
	})
}