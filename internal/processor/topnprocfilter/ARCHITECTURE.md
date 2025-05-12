# Top-N Process Filter Architecture

This document describes the internal architecture and implementation details of the Top-N Process Filter processor.

## Architecture Overview

The Top-N Process Filter processor is designed as a modular, high-performance OpenTelemetry processor for filtering process metrics. Its main goal is to reduce the volume of telemetry data by focusing on the most resource-intensive processes.

### Core Components

```
+---------------------+
| topNProcessor       |
+---------------------+
| - Process tracking  |
| - Top-N selection   |
| - Process filtering |
| - Metric reporting  |
+---------------------+
        |
        v
+---------------------+       +---------------------+
| Caching System      | <---> | Concurrent Processing|
+---------------------+       +---------------------+
        |                             |
        v                             v
+---------------------+       +---------------------+
| Memory Optimization | <---> | Metrics Optimization|
+---------------------+       +---------------------+
```

## Implementation Details

### Files and Responsibility

- `processor.go`: Main processor implementation, process tracking and filtering
- `processor_concurrent.go`: Concurrent processing implementation with worker pools
- `processor_metrics.go`: Metrics reporting and optimization
- `config.go`: Configuration definition and validation
- `factory.go`: Processor factory for integration with OpenTelemetry

### Key Data Structures

#### Process Metric Structure

```go
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
```

#### Top-N Processor

```go
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
    
    // Metrics reporting optimizations
    metricsReportingInterval time.Duration
    metricsBuffer            chan metricUpdate
    metricsErrorCount        atomic.Int64
    metricsCircuitOpen       atomic.Bool
    metricsCircuitResetTime  atomic.Int64
    metricsBatchSize         int
    metricsWg                sync.WaitGroup
}
```

### Core Algorithms

#### Top-N Selection

The processor uses a heap-based algorithm for efficient top-N selection:

```go
// processHeap implements the heap.Interface for processMetric
type processHeap struct {
    items []*processMetric
    less  func(*processMetric) float64
}

// getTopNByMetric returns the top N process metrics by the given metric selector
func getTopNByMetric(processes []*processMetric, n int, metricSelector func(*processMetric) float64) []*processMetric {
    if len(processes) <= n {
        return processes
    }
    
    // Use a min-heap to maintain the top N elements
    h := &processHeap{
        items: make([]*processMetric, 0, n),
        less:  metricSelector,
    }
    heap.Init(h)
    
    // Process the first N elements
    for i := 0; i < n; i++ {
        heap.Push(h, processes[i])
    }
    
    // For each remaining element, if it's greater than the smallest element in the heap,
    // remove the smallest and add this element
    for i := n; i < len(processes); i++ {
        if metricSelector(processes[i]) > metricSelector(h.items[0]) {
            heap.Pop(h)
            heap.Push(h, processes[i])
        }
    }
    
    return h.items
}
```

#### Concurrent Processing

The processor implements a worker pool pattern with semaphores for controlled concurrency:

```go
// processMetricsConcurrent implements the core top-N filtering logic
// with concurrent processing for better performance
func (tp *topNProcessor) processMetricsConcurrent(ctx context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
    // Create channels for work distribution and result collection
    resourceMetricsCh := make(chan resourceMetricWork, md.ResourceMetrics().Len())
    resultsCh := make(chan resourceMetricResult, md.ResourceMetrics().Len())
    
    // Create a wait group to track worker completion
    var wg sync.WaitGroup
    
    // Start worker goroutines
    workerCount := min(tp.concurrencyLimit, md.ResourceMetrics().Len())
    for i := 0; i < workerCount; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for work := range resourceMetricsCh {
                // Process the resource metrics (worker logic)
                // ...
                resultsCh <- result
            }
        }()
    }
    
    // Queue work items
    for i := 0; i < md.ResourceMetrics().Len(); i++ {
        resourceMetricsCh <- resourceMetricWork{
            index: i,
            rm:    md.ResourceMetrics().At(i),
        }
    }
    
    // Close the work channel and wait for all workers to complete
    close(resourceMetricsCh)
    wg.Wait()
    close(resultsCh)
    
    // Process results
    // ...
}
```

#### Metrics Batching

The processor uses a channel-based buffer with batch processing for efficient metrics reporting:

```go
// processMetricsUpdates processes metrics updates in batches
func (tp *topNProcessor) processMetricsUpdates() {
    defer tp.metricsWg.Done()
    
    batch := make([]metricUpdate, 0, tp.metricsBatchSize)
    ticker := time.NewTicker(100 * time.Millisecond) // Process batches every 100ms
    
    for {
        select {
        case update, ok := <-tp.metricsBuffer:
            if !ok {
                // Channel closed, process remaining updates and exit
                if len(batch) > 0 {
                    tp.processBatch(batch)
                }
                return
            }
            
            batch = append(batch, update)
            if len(batch) >= tp.metricsBatchSize {
                tp.processBatch(batch)
                batch = batch[:0] // Clear batch but keep capacity
            }
            
        // ... other cases
        }
    }
}
```

### Optimization Techniques

#### 1. Memory Optimization

- Using int64 Unix timestamps instead of time.Time objects (~70% memory reduction for timestamp fields)
- Efficient in-place filtering to avoid unnecessary allocations
- Careful use of slices and maps to minimize GC pressure

#### 2. Caching System

The caching system maintains a copy of the latest top-N computation result and invalidates it when processes change:

```go
// getTopNProcesses returns the top N processes by CPU and memory usage
func (tp *topNProcessor) getTopNProcesses() []*processMetric {
    tp.topNCacheMu.RLock()
    if tp.topNCacheValid {
        tp.cacheHitCount.Add(1)
        result := tp.topNCache
        tp.topNCacheMu.RUnlock()
        return result
    }
    tp.topNCacheMu.RUnlock()
    
    tp.cacheMissCount.Add(1)
    
    // Cache miss, compute top-N
    return tp.updateTopNCache()
}

// updateTopNCache recomputes the top-N processes and updates the cache
func (tp *topNProcessor) updateTopNCache() []*processMetric {
    // ... compute top-N processes
    
    tp.topNCacheMu.Lock()
    defer tp.topNCacheMu.Unlock()
    
    tp.topNCache = result
    tp.topNCacheValid = true
    
    // Update PID cache for fast lookups
    tp.topNPIDCache = make(map[string]struct{}, len(result))
    for _, proc := range result {
        tp.topNPIDCache[proc.PID] = struct{}{}
    }
    
    return result
}
```

#### 3. Circuit Breaker Pattern

The metrics reporting system implements a circuit breaker pattern to prevent cascading failures:

```go
// Check if we need to open the circuit breaker
if errorCount > 0 {
    currentErrors := tp.metricsErrorCount.Add(int64(errorCount))
    if currentErrors >= circuitBreakerThreshold {
        tp.metricsCircuitOpen.Store(true)
        tp.metricsCircuitResetTime.Store(time.Now().Add(time.Second * circuitBreakerResetInterval).Unix())
        tp.logger.Warn("Metrics circuit breaker opened due to repeated errors",
            zap.Int64("error_count", currentErrors),
            zap.Int("threshold", circuitBreakerThreshold),
            zap.Int("reset_seconds", circuitBreakerResetInterval))
    }
} else {
    // Reset error count on successful batch
    tp.metricsErrorCount.Store(0)
}
```

## Processing Flow

1. **Metric Batch Reception**: Processor receives a batch of metrics via the `processMetrics` method
2. **Concurrency Decision**: Based on batch size, it routes to either concurrent or sequential processing
3. **Resource Metrics Processing**: For each resource metric:
   - Extract process attributes (PID, name)
   - Extract resource utilization metrics
   - Update the internal process tracking map
4. **Top-N Selection**: Based on updated process metrics, compute or use cached top-N processes
5. **Metric Filtering**: Filter input metrics to only include the top-N processes
6. **Metrics Reporting**: Asynchronously report processor metrics using the buffered system

## Configuration and Tuning

The processor provides extensive configuration options:

```yaml
processors:
  topnprocfilter:
    top_n: 50                      # Number of top processes to retain
    cpu_threshold: 0.01            # Minimum CPU usage (1%)
    memory_threshold: 0.01         # Minimum memory usage (1%)
    idle_ttl: 5m                   # How long to keep idle processes
    registry_id: proc_top_n        # ID for tunable registry
    metrics_reporting_interval: 10s # How often to report metrics
    metrics_batch_size: 10         # Max metrics to batch together
    metrics_buffer_size: 100       # Size of metrics buffer channel
```

## Extension Points

1. **New Metrics**: Additional metrics can be added by extending the `initMetrics` method
2. **Additional Filters**: New filtering criteria can be added by extending the process selection logic
3. **Custom Thresholds**: More threshold types can be added (e.g., disk I/O, network usage)
4. **Dynamic Configuration**: Additional tunable parameters can be exposed via the registry

## Performance Characteristics

- **Time Complexity**: O(n log k) for top-N selection where n is the number of processes and k is the top-N value
- **Space Complexity**: O(n) for process tracking, where n is the number of active processes
- **Concurrency**: Scales with available CPU cores for large batch processing
- **Throughput**: Can handle thousands of process metrics per second with minimal overhead