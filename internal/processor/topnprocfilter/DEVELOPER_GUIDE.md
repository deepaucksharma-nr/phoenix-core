# Top-N Process Filter Developer Guide

This guide is intended for developers who want to modify, extend, or contribute to the Top-N Process Filter processor.

## Development Environment Setup

1. Clone the repository
2. Install Go (version 1.20 or higher)
3. Run `go mod tidy` to install dependencies
4. Run tests with `go test ./internal/processor/topnprocfilter/...`

## Code Structure

The processor is organized into several key files:

```
internal/processor/topnprocfilter/
├── config.go             # Configuration definition and validation
├── factory.go            # Processor factory for OpenTelemetry integration
├── processor.go          # Core processor implementation
├── processor_concurrent.go # Concurrent processing implementation
├── processor_metrics.go  # Metrics reporting implementation
├── processor_test.go     # Tests
├── README.md             # User documentation
├── ARCHITECTURE.md       # Architecture documentation
└── DEVELOPER_GUIDE.md    # This file
```

## Key Interfaces and Extension Points

### Adding New Metrics

To add new metrics to the processor, modify the `initMetrics` method in `processor_metrics.go`:

```go
// Add a new metric
_, err = tp.metricsRegistry.GetOrCreateGauge(
    "pte_topn_new_metric",
    "Description of the new metric",
    metrics.UnitCount,
)
if err != nil {
    return err
}
```

Then update the `reportMetrics` method to report values for your new metric:

```go
// In reportMetrics method
tp.submitMetricUpdate(
    "pte_topn_new_metric",
    float64(someValue),
    attrs,
    "gauge", // or "counter"
)
```

### Adding New Process Selection Criteria

To add a new criterion for process selection (beyond CPU and memory usage):

1. Add new fields to the `processMetric` struct:

```go
type processMetric struct {
    // Existing fields...
    NewMetricValue float64 // Add your new metric
}
```

2. Update the process tracking logic to populate your new metric:

```go
// In extractProcessMetrics or similar function
proc.NewMetricValue = extractNewMetricValue(metrics)
```

3. Add a new selector function for the top-N algorithm:

```go
topNByNewMetric := getTopNByMetric(processes, n, func(p *processMetric) float64 { 
    return p.NewMetricValue 
})
```

4. Modify the `getTopNProcesses` method to include your new criterion in the selection.

### Implementing a New Optimization

To implement a new optimization technique:

1. Create a new file for your optimization (e.g., `processor_optimization.go`)
2. Design your optimization to be orthogonal to existing optimizations
3. Add configuration options to `config.go`
4. Add initialization in the `newProcessor` or `Start` methods
5. Update documentation to describe your optimization

## Recommended Patterns

### Processor Extension

To extend the processor with new features while maintaining backward compatibility:

```go
// Create a wrapper structure
type extendedProcessor struct {
    *topNProcessor
    newFeatureField type
}

// Override methods as needed
func (ep *extendedProcessor) processMetrics(ctx context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
    // Add pre-processing logic
    
    // Call the original method
    result, err := ep.topNProcessor.processMetrics(ctx, md)
    if err != nil {
        return result, err
    }
    
    // Add post-processing logic
    
    return result, nil
}
```

### Concurrent Processing

When implementing concurrent operations, follow these patterns:

1. Use channels for work distribution and results collection
2. Use sync.WaitGroup to track worker completion
3. Implement a graceful shutdown mechanism
4. Use semaphores (channels) for concurrency limiting
5. Prefer atomic operations over mutexes when applicable

Example:

```go
// Semaphore pattern for concurrency control
sem := make(chan struct{}, concurrencyLimit)

for i := 0; i < workItems; i++ {
    sem <- struct{}{} // Acquire semaphore
    wg.Add(1)
    
    go func(item workItem) {
        defer func() {
            <-sem // Release semaphore
            wg.Done()
        }()
        
        // Process work item
    }(items[i])
}

wg.Wait() // Wait for all workers to complete
```

### Memory Efficiency

To maintain memory efficiency:

1. Avoid unnecessary allocations in hot paths
2. Reuse slices and maps when possible
3. Be careful with closures that can capture large variables
4. Consider using sync.Pool for frequently allocated objects
5. Use slice capacity hints when the size is known

Example:

```go
// Preallocate with capacity hint
result := make([]*processMetric, 0, estimatedSize)

// Reuse slice by clearing without reallocating
batch = batch[:0]
```

## Testing

### Unit Tests

Create unit tests for all components:

```go
func TestSomeFunction(t *testing.T) {
    // Arrange
    input := createTestInput()
    expected := createExpectedOutput()
    
    // Act
    actual := someFunction(input)
    
    // Assert
    assert.Equal(t, expected, actual)
}
```

### Benchmarks

Add benchmarks for performance-critical functions:

```go
func BenchmarkTopNSelection(b *testing.B) {
    processes := createTestProcesses(1000)
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        getTopNByMetric(processes, 50, func(p *processMetric) float64 { 
            return p.CPUUsage 
        })
    }
}
```

## Performance Tuning

When optimizing the processor, focus on these areas:

1. **Process Selection Algorithm**: The core top-N selection should be as efficient as possible
2. **Memory Allocation**: Minimize allocations in hot paths
3. **Concurrency Control**: Balance parallelism with overhead
4. **Cache Efficiency**: Optimize cache invalidation logic
5. **Metrics Reporting**: Use batching and asynchronous processing

## Common Pitfalls

1. **Race Conditions**: When modifying the process map or caches, ensure proper synchronization
2. **Memory Leaks**: Ensure old processes are cleaned up via the idle TTL mechanism
3. **Configuration Validation**: Validate all config parameters to avoid runtime errors
4. **Metrics Buffer Overflow**: Handle buffer full conditions gracefully
5. **Context Propagation**: Ensure contexts are properly passed through processing chain

## Adding New Configuration Parameters

1. Add the parameter to the `Config` struct in `config.go`
2. Add validation in the `Validate` method
3. Add a default value in `createDefaultConfig`
4. Update the processor to use the new parameter
5. Document the parameter in README.md

Example:

```go
// In config.go
type Config struct {
    // Existing fields...
    
    // NewParameter is a new configuration parameter
    NewParameter int `mapstructure:"new_parameter"`
}

// In Validate method
if cfg.NewParameter < 0 {
    return fmt.Errorf("new_parameter must be non-negative, got %d", cfg.NewParameter)
}

// In createDefaultConfig
return &Config{
    // Existing defaults...
    NewParameter: 42,
}
```

## Contributing Guidelines

1. Follow Go best practices and code style
2. Add tests for all new functionality
3. Update documentation with any changes
4. Ensure backward compatibility when possible
5. Benchmark before and after performance-critical changes
6. Add explanatory comments for complex logic