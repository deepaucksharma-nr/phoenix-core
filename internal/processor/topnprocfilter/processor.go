package topnprocfilter

import (
	"container/heap"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/deepaucksharma-nr/phoenix-core/internal/metrics"
	"github.com/deepaucksharma-nr/phoenix-core/internal/pkg/tunableregistry"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

// Mock processor.CreateSettings for testing
type processorCreateSettings struct {
	TelemetrySettings component.TelemetrySettings
	BuildInfo         component.BuildInfo
	Logger            *zap.Logger
}

// processMetric is a structure holding metric values for a specific process
// Optimized for memory efficiency by using time.Unix values instead of time.Time objects
type processMetric struct {
	PID                   string
	ProcessName           string
	CPUUsage              float64
	MemoryUsage           float64
	LastUpdatedUnix       int64  // Unix timestamp instead of time.Time to reduce memory usage
	LastAboveThresholdUnix int64 // Unix timestamp instead of time.Time to reduce memory usage
}

// topNProcessor implements top-N filtering for process metrics
type topNProcessor struct {
	logger    *zap.Logger
	config    *Config
	processes sync.Map  // stores processMetric by PID
	n         int       // current top-N value, can be adjusted via registry
	mu        sync.Mutex

	// Top-N caching system
	topNCache      []*processMetric  // caches the result of getTopNProcesses
	topNCacheMu    sync.RWMutex      // mutex for topNCache
	topNCacheValid bool              // indicates if cache is valid

	// Fast PID lookup cache for isInTopN (populated with each topNCache update)
	topNPIDCache   map[string]struct{}

	// Metrics
	metricsRegistry     *metrics.MetricsRegistry
	metricsTicker       *time.Ticker
	stopCh              chan struct{}
	processCount        atomic.Int64
	filteredCount       atomic.Int64
	currentCPUThreshold atomic.Float64
	currentMemThreshold atomic.Float64
	cacheHitCount       atomic.Int64
	cacheMissCount      atomic.Int64

	// Concurrency control
	concurrencyLimit int
	workerSemaphore  chan struct{}

	// Metrics reporting optimizations
	metricsReportingInterval time.Duration
	metricsBuffer            chan metricUpdate
	metricsErrorCount        atomic.Int64
	metricsCircuitOpen       atomic.Bool
	metricsCircuitResetTime  atomic.Int64
	metricsBatchSize         int
	metricsWg                sync.WaitGroup
}

// newProcessor creates a new topN process metrics filter processor
func newProcessor(set processorCreateSettings, config *Config) (*topNProcessor, error) {
	idleTTL, err := time.ParseDuration(config.IdleTTL)
	if err != nil {
		return nil, err
	}

	// Parse metrics reporting interval
	metricsReportingInterval := defaultMetricsReportingInterval
	if config.MetricsReportingInterval != "" {
		parsed, err := time.ParseDuration(config.MetricsReportingInterval)
		if err == nil {
			metricsReportingInterval = parsed
		}
	}

	// Use configured batch size if provided
	metricsBatchSize := defaultMetricsBatchSize
	if config.MetricsBatchSize > 0 {
		metricsBatchSize = config.MetricsBatchSize
	}

	// Use configured buffer size if provided
	metricsBufferSize := defaultMetricsBufferSize
	if config.MetricsBufferSize > 0 {
		metricsBufferSize = config.MetricsBufferSize
	}

	proc := &topNProcessor{
		logger:                   set.Logger,
		config:                   config,
		n:                        config.TopN,
		metricsRegistry:          metrics.GetInstance(set.Logger),
		stopCh:                   make(chan struct{}),
		topNPIDCache:             make(map[string]struct{}, config.TopN*2), // Initialize PID cache
		topNCacheValid:           false, // Cache starts invalid
		metricsReportingInterval: metricsReportingInterval,
		metricsBatchSize:         metricsBatchSize,
		metricsBuffer:            make(chan metricUpdate, metricsBufferSize),
	}

	// Initialize thresholds for metrics
	proc.currentCPUThreshold.Store(config.CPUThreshold)
	proc.currentMemThreshold.Store(config.MemoryThreshold)

	// Initialize metrics
	if err := proc.initMetrics(); err != nil {
		return nil, fmt.Errorf("failed to initialize metrics: %w", err)
	}

	// Add cache metrics
	if _, err := proc.metricsRegistry.GetOrCreateCounter(
		"pte_topn_cache_hit_total",
		"Total number of cache hits in the top-N processor",
		metrics.UnitCount,
	); err != nil {
		return nil, fmt.Errorf("failed to initialize cache hit counter: %w", err)
	}

	if _, err := proc.metricsRegistry.GetOrCreateCounter(
		"pte_topn_cache_miss_total",
		"Total number of cache misses in the top-N processor",
		metrics.UnitCount,
	); err != nil {
		return nil, fmt.Errorf("failed to initialize cache miss counter: %w", err)
	}

	// Register with TunableRegistry
	registry := tunableregistry.GetInstance()
	registry.Register(proc)

	proc.logger.Info("Top-N process metrics filter processor created",
		zap.Int("top_n", config.TopN),
		zap.Float64("cpu_threshold", config.CPUThreshold),
		zap.Float64("memory_threshold", config.MemoryThreshold),
		zap.String("idle_ttl", config.IdleTTL),
		zap.Bool("caching_enabled", true))

	// Start cleanup goroutine
	go proc.periodicCleanup(idleTTL)

	return proc, nil
}

// ID implements the Tunable interface
func (tp *topNProcessor) ID() string {
	return tp.config.RegistryID
}

// SetValue implements the Tunable interface
func (tp *topNProcessor) SetValue(key string, value float64) {
	if key == "top_n" {
		tp.setTopN(int(value))
	} else if key == "cpu_threshold" {
		// Could add support for dynamically adjusting thresholds
		tp.logger.Debug("Adjusting CPU threshold not yet implemented", zap.Float64("value", value))
	} else if key == "memory_threshold" {
		// Could add support for dynamically adjusting thresholds
		tp.logger.Debug("Adjusting memory threshold not yet implemented", zap.Float64("value", value))
	}
	// Ignore other keys
}

// GetValue implements the Tunable interface
func (tp *topNProcessor) GetValue(key string) float64 {
	if key == "top_n" {
		tp.mu.Lock()
		defer tp.mu.Unlock()
		return float64(tp.n)
	} else if key == "top_n_ratio" {
		tp.mu.Lock()
		defer tp.mu.Unlock()
		return float64(tp.n) / float64(tp.config.TopN)
	} else if key == "cpu_threshold" {
		return tp.config.CPUThreshold
	} else if key == "memory_threshold" {
		return tp.config.MemoryThreshold
	}
	// Return 0 for unknown keys
	return 0
}

// setTopN sets the top-N value
func (tp *topNProcessor) setTopN(newN int) {
	if newN < 1 {
		newN = 1 // Ensure we keep at least 1 process
	}

	tp.mu.Lock()
	oldN := tp.n
	tp.n = newN
	tp.mu.Unlock()

	// If N changed, invalidate the cache
	if oldN != newN {
		tp.invalidateTopNCache()
		tp.logger.Info("Top-N value updated, cache invalidated",
			zap.Int("new_n", newN),
			zap.Int("old_n", oldN))
	}
}

// SetProbability implements the ProbSampler interface (backward compatibility)
func (tp *topNProcessor) SetProbability(p float64) {
	// Reinterpret probability as a percentage of the default N
	newN := int(p * float64(tp.config.TopN))
	tp.SetValue("top_n", float64(newN))
}

// GetProbability implements the ProbSampler interface (backward compatibility)
func (tp *topNProcessor) GetProbability() float64 {
	return tp.GetValue("top_n_ratio")
}

// Implemented in processor_metrics.go
// func (tp *topNProcessor) Start(ctx context.Context, host component.Host) error {
//     return nil
// }

// Implemented in processor_metrics.go
// func (tp *topNProcessor) Shutdown(ctx context.Context) error {
//     return nil
// }

// processMetrics implements the core top-N filtering logic
// processMetrics implements the core top-N filtering logic
// The main implementation is in processor_concurrent.go for better maintainability
func (tp *topNProcessor) processMetrics(ctx context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
	// Note: The concurrent implementation is in processor_concurrent.go
	// This function is just a wrapper for the actual implementation
	return tp.processMetricsConcurrent(ctx, md)
}

// processPriorityQueue implements a min-heap for process metrics
// used in the optimized top-N algorithm
type processPriorityQueue struct {
	items []*processMetric
	less  func(p *processMetric) float64
}

// Implement heap.Interface methods for processPriorityQueue
func (pq *processPriorityQueue) Len() int { return len(pq.items) }
func (pq *processPriorityQueue) Swap(i, j int) { pq.items[i], pq.items[j] = pq.items[j], pq.items[i] }
func (pq *processPriorityQueue) Less(i, j int) bool {
	return pq.less(pq.items[i]) < pq.less(pq.items[j])
}

func (pq *processPriorityQueue) Push(x interface{}) {
	pq.items = append(pq.items, x.(*processMetric))
}

func (pq *processPriorityQueue) Pop() interface{} {
	old := pq.items
	n := len(old)
	item := old[n-1]
	pq.items = old[0 : n-1]
	return item
}

// getTopNProcesses returns the top N processes by CPU and memory usage
// using an optimized heap-based algorithm with caching
func (tp *topNProcessor) getTopNProcesses() []*processMetric {
	// Check if cache is valid
	tp.topNCacheMu.RLock()
	if tp.topNCacheValid && tp.topNCache != nil {
		result := tp.topNCache // Use cached result
		tp.topNCacheMu.RUnlock()
		tp.cacheHitCount.Add(1)

		// Report cache hit
		updateErr := tp.metricsRegistry.UpdateCounter(
			context.Background(),
			"pte_topn_cache_hit_total",
			1.0,
			map[string]string{"processor": "topnprocfilter"},
		)
		if updateErr != nil {
			tp.logger.Debug("Failed to update cache hit metric", zap.Error(updateErr))
		}

		return result
	}
	tp.topNCacheMu.RUnlock()

	// Cache miss, report it
	tp.cacheMissCount.Add(1)
	updateErr := tp.metricsRegistry.UpdateCounter(
		context.Background(),
		"pte_topn_cache_miss_total",
		1.0,
		map[string]string{"processor": "topnprocfilter"},
	)
	if updateErr != nil {
		tp.logger.Debug("Failed to update cache miss metric", zap.Error(updateErr))
	}

	// Get current active processes count for capacity preallocation
	var processCount int
	tp.processes.Range(func(_, _ interface{}) bool {
		processCount++
		return true
	})

	tp.mu.Lock()
	n := tp.n
	tp.mu.Unlock()

	// Preallocate with known capacity
	processes := make([]*processMetric, 0, processCount)
	tp.processes.Range(func(_, value interface{}) bool {
		processes = append(processes, value.(*processMetric))
		return true
	})

	if len(processes) <= n {
		// Update cache with all processes
		tp.updateTopNCache(processes)
		return processes // Return all if we have fewer than N
	}

	// Create top-N PIDs map for efficient lookups later
	topNPIDs := make(map[string]struct{}, n*2) // Pre-allocate for both CPU and memory

	// Get top-N by CPU usage using heap
	cpuTopN := getTopNByMetricOptimized(processes, n, func(p *processMetric) float64 {
		return p.CPUUsage
	})

	// Get top-N by memory usage using heap
	memTopN := getTopNByMetricOptimized(processes, n, func(p *processMetric) float64 {
		return p.MemoryUsage
	})

	// Add CPU top-N to the result and track PIDs
	result := make([]*processMetric, 0, n*2) // Pre-allocate for worst case
	for _, proc := range cpuTopN {
		topNPIDs[proc.PID] = struct{}{}
		result = append(result, proc)
	}

	// Add memory top-N if not already added
	for _, proc := range memTopN {
		if _, exists := topNPIDs[proc.PID]; !exists {
			topNPIDs[proc.PID] = struct{}{}
			result = append(result, proc)
		}
	}

	// Update cache with computed results
	tp.updateTopNCache(result)

	// Also update the PID lookup cache
	tp.topNCacheMu.Lock()
	tp.topNPIDCache = topNPIDs
	tp.topNCacheMu.Unlock()

	return result
}

// getTopNByMetricOptimized returns the top N processes by a specific metric
// using a min-heap algorithm for better efficiency
func getTopNByMetricOptimized(processes []*processMetric, n int, valueFn func(*processMetric) float64) []*processMetric {
	if len(processes) <= n {
		return processes
	}

	// Create a min-heap
	h := &processPriorityQueue{
		items: make([]*processMetric, 0, n),
		less:  valueFn,
	}

	// Initialize with first n elements
	for i := 0; i < n && i < len(processes); i++ {
		heap.Push(h, processes[i])
	}

	// Make sure we have items before continuing
	if h.Len() == 0 {
		return processes[:0] // Return empty slice if no processes
	}

	// Process remaining elements - only keep those larger than the minimum in heap
	minValue := valueFn(h.items[0]) // Current minimum value in heap

	for i := n; i < len(processes); i++ {
		// Quick check to avoid heap operations if not needed
		currValue := valueFn(processes[i])
		if currValue > minValue {
			heap.Pop(h)
			heap.Push(h, processes[i])
			minValue = valueFn(h.items[0]) // Update current minimum
		}
	}

	// Convert heap to slice (in ascending order)
	result := make([]*processMetric, h.Len())
	for i := len(result) - 1; i >= 0; i-- {
		result[i] = heap.Pop(h).(*processMetric)
	}

	return result
}

// quickSelect implements the QuickSelect algorithm to find the kth largest element
// This is kept for reference but is no longer used in the optimized implementation
func quickSelect(arr []*processMetric, left, right, k int, valueFn func(*processMetric) float64) {
	if left == right {
		return
	}

	pivotIndex := partition(arr, left, right, valueFn)

	if k == pivotIndex {
		return
	} else if k < pivotIndex {
		quickSelect(arr, left, pivotIndex-1, k, valueFn)
	} else {
		quickSelect(arr, pivotIndex+1, right, k, valueFn)
	}
}

// partition partitions the array around a pivot
// This is kept for reference but is no longer used in the optimized implementation
func partition(arr []*processMetric, left, right int, valueFn func(*processMetric) float64) int {
	// Choose a pivot (rightmost element)
	pivotValue := valueFn(arr[right])

	// Index of smaller element
	i := left - 1

	for j := left; j < right; j++ {
		// If current element is larger than pivot
		if valueFn(arr[j]) > pivotValue {
			i++
			arr[i], arr[j] = arr[j], arr[i]
		}
	}

	// Place pivot in its correct position
	arr[i+1], arr[right] = arr[right], arr[i+1]
	return i + 1
}

// isInTopN checks if a PID is in the top-N list using either map lookup (fast)
// or linear search (fallback)
func (tp *topNProcessor) isInTopN(pid string) bool {
	// Try the cache lookup first (O(1) operation)
	tp.topNCacheMu.RLock()
	if tp.topNPIDCache != nil {
		_, exists := tp.topNPIDCache[pid]
		tp.topNCacheMu.RUnlock()
		return exists
	}

	// If no PID cache is available, use the slower linear search on the topNCache
	if tp.topNCache != nil {
		for _, proc := range tp.topNCache {
			if proc.PID == pid {
				tp.topNCacheMu.RUnlock()
				return true
			}
		}
	}
	tp.topNCacheMu.RUnlock()

	// If we get here and cache isn't valid, we need to recompute
	if !tp.topNCacheValid {
		_ = tp.getTopNProcesses() // This will rebuild the cache

		// After cache is built, use it
		tp.topNCacheMu.RLock()
		_, exists := tp.topNPIDCache[pid]
		tp.topNCacheMu.RUnlock()
		return exists
	}

	return false
}

// updateTopNCache updates the cached top-N processes list
func (tp *topNProcessor) updateTopNCache(processes []*processMetric) {
	tp.topNCacheMu.Lock()
	defer tp.topNCacheMu.Unlock()

	// Create a new copy to avoid race conditions with the original slice
	tp.topNCache = make([]*processMetric, len(processes))
	copy(tp.topNCache, processes)

	// Mark cache as valid
	tp.topNCacheValid = true
}

// invalidateTopNCache marks the top-N cache as invalid
// Called when processes are updated or when top-N value changes
func (tp *topNProcessor) invalidateTopNCache() {
	tp.topNCacheMu.Lock()
	tp.topNCacheValid = false
	tp.topNCacheMu.Unlock()
}

// periodicCleanup removes stale process entries that haven't been updated or above threshold
func (tp *topNProcessor) periodicCleanup(idleTTL time.Duration) {
	ticker := time.NewTicker(idleTTL / 2)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()

		// Cleanup processes that haven't been above threshold for idleTTL
		var toRemove []string
		var processChanged bool

		tp.processes.Range(func(key, value interface{}) bool {
			pid := key.(string)
			proc := value.(*processMetric)

			// Check last time process was above threshold
			// Convert Unix timestamp to time.Time for comparison
			lastAboveThreshold := time.Unix(proc.LastAboveThresholdUnix, 0)
			if now.Sub(lastAboveThreshold) > idleTTL {
				toRemove = append(toRemove, pid)
				processChanged = true
			}

			return true
		})

		// Remove stale processes
		for _, pid := range toRemove {
			tp.processes.Delete(pid)
		}

		// Invalidate cache if processes changed
		if processChanged {
			tp.invalidateTopNCache()
		}

		if len(toRemove) > 0 {
			tp.logger.Debug("Cleaned up idle processes",
				zap.Int("removed_count", len(toRemove)))
		}
	}
}