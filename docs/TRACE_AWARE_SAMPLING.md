# Trace-Aware Reservoir Sampling

## Introduction

Phoenix Telemetry Edge (PTE) supports trace-aware reservoir sampling, which enhances
the standard reservoir sampling mechanism by treating traces as cohesive units rather
than individual spans. This document explains the concept, implementation, and configuration
of trace-aware sampling.

## The Problem with Standard Span-Level Sampling

In standard reservoir sampling, each span is treated as an independent entity. This can
lead to several issues:

1. **Fragmented Traces**: Parent spans might be sampled while their children are not, or vice versa
2. **Loss of Context**: Critical relationships between spans are lost, making it difficult to understand the full call path
3. **Reduced Analytical Value**: Without complete traces, it becomes harder to diagnose performance issues or understand system behavior

## Trace-Aware Sampling Solution

Trace-aware sampling addresses these issues by treating an entire trace as a single unit for
sampling decisions. Key aspects of the implementation include:

1. **Span Buffering by TraceID**: Spans are grouped into "trace buffers" based on their TraceID
2. **Root Span Detection**: A trace is considered complete when it contains at least one root span (a span with no parent)
3. **Timeout Mechanism**: Incomplete traces are processed after a configurable timeout to prevent memory leaks
4. **Whole-Trace Sampling**: The entire trace (all spans) is sampled or discarded as a single unit
5. **Statistical Correctness**: Algorithm R is applied at the trace level, maintaining a representative sample

## Benefits

- **Complete Traces**: All spans from a trace stay together, providing full context
- **Better Analysis**: More useful data for debugging and performance analysis
- **Statistical Validity**: Each trace has the same probability of being selected
- **Efficient Memory Use**: Timeout mechanism prevents unbounded growth

## Configuration

To enable trace-aware sampling, use the following configuration options:

```yaml
processors:
  reservoir_sampler:
    size_k: 5000                   # Size of the reservoir (in traces, not spans)
    window_duration: "60s"         # Window duration for the sampler
    checkpoint_path: "/data/pte/reservoir.db"  # Path to persistent storage
    checkpoint_interval: "10s"     # How often to save state
    trace_aware: true              # Enable trace-aware sampling
    trace_timeout: "5s"            # How long to wait for a trace to complete
```

### Configuration Parameters

| Parameter | Description | Default |
|-----------|-------------|---------|
| `trace_aware` | Enables trace-aware sampling when set to true | `true` |
| `trace_timeout` | Maximum time to wait for a trace to complete before sampling it | `"5s"` |

## Performance Considerations

Trace-aware sampling has some performance implications to consider:

1. **Memory Usage**: Spans are buffered until a trace is complete or times out, which increases memory usage
2. **Processing Delay**: Spans may be held in the buffer until the trace completes or times out
3. **Concurrency**: The implementation uses locks to manage concurrent access to the trace buffers

For high-volume systems, you may need to:

1. Reduce the `trace_timeout` value to decrease the amount of time spans are held in memory
2. Increase the memory allocation for the PTE service
3. Consider using standard span-level sampling if the trace-aware overhead is too high

## Monitoring

The following metrics are available to monitor trace-aware sampling:

- `pte.reservoir.complete_traces`: Number of complete traces processed (with root span)
- `pte.reservoir.timed_out_traces`: Number of traces that timed out without being complete
- `pte.reservoir.incomplete_traces`: Number of traces currently in the buffer

## Example Configuration

See the [example configuration](examples/trace-aware-reservoir-sampling.yaml) for a complete
example of how to set up trace-aware sampling.

## Tuning Parameters

The `trace_timeout` parameter is crucial for balancing memory usage against trace completeness:

- **Lower values** (e.g., "1s") reduce memory usage but may lead to more incomplete traces
- **Higher values** (e.g., "10s") increase the chance of capturing complete traces but use more memory

Set this based on your typical trace completion times and memory constraints.

## Best Practices

1. Start with the default timeout (5s) and adjust based on your observed trace patterns
2. Monitor the `pte.reservoir.timed_out_traces` metric - a high value may indicate the timeout is too low
3. For high-throughput systems, consider using separate pipelines with different sampling strategies
4. Remember that sampling rates apply to traces rather than spans when trace-aware sampling is enabled