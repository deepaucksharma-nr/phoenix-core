# Top-N Process Filter Processor

The Top-N Process Filter Processor is a specialized OpenTelemetry processor that filters process metrics to focus on the most resource-intensive processes. It helps reduce the volume of telemetry data by only forwarding metrics for processes that meet specific CPU and memory utilization thresholds, or are among the top N resource consumers.

## Features

- **Smart Process Selection**: Track only the top N processes by CPU or memory usage
- **Threshold-Based Filtering**: Filter out processes below configurable CPU and memory thresholds
- **High-Performance Implementation**: Uses efficient algorithms and memory optimization
- **Caching Support**: Caches computed results to reduce redundant processing
- **Concurrent Processing**: Scales to handle large volumes of metrics using parallel processing
- **Configurable Metrics Reporting**: Optimized metrics collection with batching and circuit breaker patterns
- **Tunable Parameters**: Dynamic configuration adjustments at runtime

## How It Works

### Process Selection and Tracking

The processor maintains an internal map of process metrics, indexed by PID. When metrics are processed:

1. Metrics are scanned for process resource attributes (process.pid, etc.)
2. Resource utilization metrics (CPU, memory) are extracted and stored
3. Processes below the configured thresholds are filtered out
4. Only the top N processes by CPU or memory usage are retained in the output
5. Idle processes are automatically removed after a configurable TTL period

### Optimization Features

#### Memory Optimization

- Uses int64 Unix timestamps instead of time.Time objects for reduced memory footprint
- Efficient data structures to minimize allocations and garbage collection overhead
- Avoids unnecessary copies of large data structures

#### Caching

- Caches the results of top-N computations
- Cache is invalidated when process metrics are updated
- Includes cache hit/miss ratio metrics for monitoring performance

#### Concurrent Processing

- Implements worker pool pattern with semaphores for controlled concurrency
- Parallel processing of metrics for improved throughput on multi-core systems
- Automatic scaling between sequential and concurrent processing based on batch size

#### Metrics Reporting Optimizations

- Batched metrics updates for reduced overhead
- Non-blocking buffer for metric operations
- Circuit breaker pattern to prevent metrics reporting failures from affecting processing
- Configurable reporting frequency and buffer sizes

### Memory Usage Comparison

Memory usage per process record:

| Field              | Before  | After   | Savings |
|--------------------|---------|---------|---------|
| PID                | ~16-24 bytes | ~16-24 bytes | 0 bytes |
| ProcessName        | ~16-24 bytes | ~16-24 bytes | 0 bytes |
| CPUUsage           | 8 bytes | 8 bytes | 0 bytes |
| MemoryUsage        | 8 bytes | 8 bytes | 0 bytes |
| LastUpdated        | 24+ bytes | - | 24+ bytes |
| LastAboveThreshold | 24+ bytes | - | 24+ bytes |
| LastUpdatedUnix    | - | 8 bytes | -8 bytes |
| LastAboveThresholdUnix | - | 8 bytes | -8 bytes |
| **Total**          | **~88-104+ bytes** | **~56-72 bytes** | **~32+ bytes (~30-40% reduction)** |

## Configuration

```yaml
processors:
  topnprocfilter:
    # Number of top processes to retain (by CPU or memory usage)
    top_n: 50
    
    # Minimum CPU utilization threshold (0.0-1.0)
    cpu_threshold: 0.01
    
    # Minimum memory utilization threshold (0.0-1.0)
    memory_threshold: 0.01
    
    # How long to keep a process in the active set after it goes below thresholds
    idle_ttl: 5m
    
    # ID for runtime tuning via registry
    registry_id: proc_top_n
    
    # Metrics reporting optimizations
    metrics_reporting_interval: 10s
    metrics_batch_size: 10
    metrics_buffer_size: 100
```

### Configuration Parameters

| Parameter | Description | Default |
|-----------|-------------|---------|
| `top_n` | Number of top processes to retain | 50 |
| `cpu_threshold` | Minimum CPU usage to include a process (0.0-1.0) | 0.01 (1%) |
| `memory_threshold` | Minimum memory usage to include a process (0.0-1.0) | 0.01 (1%) |
| `idle_ttl` | Time to keep processes after they fall below thresholds | 5m |
| `registry_id` | Identifier for runtime tuning registry | proc_top_n |
| `metrics_reporting_interval` | How often to report processor metrics | 10s |
| `metrics_batch_size` | Max metrics to batch in a single update | 10 |
| `metrics_buffer_size` | Size of the metrics buffer channel | 100 |

## Metrics

The processor exposes several metrics to monitor its operation:

| Metric | Type | Description |
|--------|------|-------------|
| `pte_process_top_n` | Gauge | Current top-N value |
| `pte_process_total` | Gauge | Total number of tracked processes |
| `pte_process_filtered` | Counter | Number of processes filtered out |
| `pte_process_threshold_cpu` | Gauge | Current CPU threshold |
| `pte_process_threshold_memory` | Gauge | Current memory threshold |
| `pte_topn_cache_hit_ratio` | Gauge | Cache hit ratio (0.0-1.0) |
| `pte_topn_cache_hit_total` | Counter | Total cache hits |
| `pte_topn_cache_miss_total` | Counter | Total cache misses |
| `pte_topn_metrics_circuit_open` | Gauge | Whether metrics circuit breaker is open (0/1) |
| `pte_topn_metrics_error_count` | Gauge | Current consecutive metrics errors |

## Runtime Tuning

The processor registers with the tunable registry, allowing dynamic adjustment of parameters at runtime:

```go
// Set the top-N value dynamically
registry.SetValue("proc_top_n", "top_n", 100)

// Adjust CPU threshold
registry.SetValue("proc_top_n", "cpu_threshold", 0.02)

// Adjust memory threshold
registry.SetValue("proc_top_n", "memory_threshold", 0.03)

// Adjust sampling probability
registry.SetValue("proc_top_n", "probability", 0.5)
```

## Example Usage

```yaml
# In your collector configuration
processors:
  topnprocfilter:
    top_n: 25
    cpu_threshold: 0.02
    memory_threshold: 0.02
    idle_ttl: 10m

service:
  pipelines:
    metrics:
      receivers: [otlp]
      processors: [topnprocfilter]
      exporters: [otlp]
```

## Performance Considerations

- **Memory Usage**: With the Unix timestamp optimization, each process consumes ~56-72 bytes of memory (versus ~88-104+ bytes previously)
- **CPU Usage**: Caching and concurrent processing reduce CPU usage, especially for large numbers of processes
- **Throughput**: The processor can handle thousands of process metrics per second with minimal overhead
- **Scaling**: For high-cardinality environments, increase the batch size and consider adjusting the concurrency limit

## Implementation Details

### Algorithms

- **Top-N Selection**: Uses a min-heap implementation for O(n log k) performance
- **Concurrency Control**: Uses semaphores and worker pools for controlled parallelism
- **Metrics Batching**: Non-blocking buffer with batch processing for efficient reporting

### Data Structures

- **Process Map**: Thread-safe map for storing process metrics by PID
- **Metrics Buffer**: Channel-based buffer for asynchronous metrics processing
- **Min-Heap**: Efficient priority queue for top-N selection

## Best Practices

1. Set appropriate thresholds based on your environment's characteristics
2. Adjust the top-N value based on how many processes you actually want to monitor
3. Tune the idle_ttl parameter to balance data retention with memory usage
4. Monitor the cache hit ratio to ensure caching is effective
5. For large deployments, increase metrics_batch_size to reduce overhead

## Troubleshooting

### High Memory Usage

- Reduce the top_n value
- Increase CPU/memory thresholds to track fewer processes
- Decrease idle_ttl to remove idle processes more quickly

### High CPU Usage

- Increase metrics_reporting_interval
- Increase metrics_batch_size
- Verify cache hit ratio is sufficiently high (>0.8)

### Missing Process Data

- Decrease CPU/memory thresholds
- Increase top_n value
- Increase idle_ttl to retain process data longer