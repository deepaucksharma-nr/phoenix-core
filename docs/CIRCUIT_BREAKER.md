# Circuit Breaker Pattern in Fallback Process Parser

## Overview

The circuit breaker pattern has been implemented in the fallback process parser to protect the telemetry pipeline from performance degradation caused by slow operations when accessing the `/proc` filesystem. This pattern provides resilience and graceful degradation by temporarily disabling operations that consistently exceed performance thresholds.

## Problem Statement

Accessing the `/proc` filesystem can occasionally be slow, especially under high system load or when interacting with processes that are themselves under stress. These slow operations can cause:

1. Increased latency in telemetry processing
2. Resource contention
3. Potential thread starvation
4. Queue buildup in the pipeline

The circuit breaker pattern addresses these issues by temporarily disabling operations that consistently perform poorly, allowing the system to maintain overall throughput even when some components are struggling.

## Implementation Details

### Configuration

The circuit breaker can be configured in the fallback process parser configuration:

```yaml
processors:
  fallback_proc_parser:
    enabled: true
    circuit_breaker_enabled: true  # Enable/disable circuit breaker pattern
    circuit_breaker_threshold_ms: 100  # Operations exceeding this threshold may trigger the circuit breaker
    circuit_breaker_cooldown_seconds: 60  # Period for which circuit remains open after being tripped
```

### Default Values

- `circuit_breaker_enabled`: `true`
- `circuit_breaker_threshold_ms`: `100` (100ms)
- `circuit_breaker_cooldown_seconds`: `60` (1 minute)

### Operation Types Monitored

The circuit breaker operates independently for each type of operation:

- `cmdline`: Reading process command line from `/proc/[pid]/cmdline`
- `username`: Resolving UID to username
- `executable_path`: Resolving executable path from `/proc/[pid]/exe`
- `io`: Reading I/O statistics from `/proc/[pid]/io`

### Circuit Breaker States

Each circuit breaker can be in one of two states:

- **Closed**: Operations are allowed to proceed normally. This is the default state.
- **Open**: Operations are temporarily blocked. The circuit breaker will automatically transition to closed after the cooldown period.

### Tripping Mechanism

The circuit breaker trips (transitions from closed to open) when:

1. Three consecutive operations exceed the configured threshold duration
2. Three consecutive operations result in errors

When a circuit breaker trips, it remains open for the configured cooldown period, after which it allows a test operation to determine if the underlying issue has been resolved.

### Fallback Behavior

When a circuit is open:

- For `username` resolution: Returns a fallback in the format `uid:[number]`
- For `cmdline`, `executable_path`, and `io` operations: Returns an error indicating the operation was blocked

### Metrics

The following metrics are used to monitor circuit breaker behavior:

- `pte_fallback_parser_circuit_breaker_trip_total`: Counts the number of times the circuit breaker has been tripped, with labels for `operation_type` and `duration_tier`
- `pte_fallback_parser_circuit_breaker_state_total`: Counts the number of state transitions (open/closed), with labels for `operation_type` and `state`
- `pte_fallback_parser_fetch_duration_ms`: Measures the duration of attribute fetch operations, which can help identify when operations are approaching the threshold

## Best Practices and Recommendations

### When to Enable Circuit Breakers

Circuit breakers are particularly useful in environments with:

- High system load
- Resource constraints
- Unstable or slow `/proc` filesystem access
- Multi-tenant environments where noisy neighbors can affect performance

### Tuning Circuit Breaker Parameters

For fine-tuning the circuit breaker behavior:

- **Threshold**: Start with a conservative value (100ms default) and adjust based on observed performance. In high-performance environments, you might decrease this to 50ms; in resource-constrained environments, you might increase to 200-300ms.

- **Cooldown Period**: The default of 60 seconds is a balanced starting point. For rapidly changing environments, consider a shorter cooldown (30 seconds); for persistent issues, consider a longer cooldown (2-5 minutes).

### Monitoring Recommendations

To effectively monitor circuit breaker behavior:

1. Set up alerts for high circuit breaker trip rates, which could indicate system-wide performance issues
2. Track operation duration metrics to preemptively identify slow operations before they trip the circuit breaker
3. Monitor the ratio of circuit open time to total time to ensure that the circuit breaker isn't causing excessive service degradation

## Example Use Cases

### High-Load Production Environment

In production environments with high throughput, the circuit breaker can prevent slow `/proc` operations from affecting overall telemetry collection:

```yaml
processors:
  fallback_proc_parser:
    circuit_breaker_enabled: true
    circuit_breaker_threshold_ms: 75  # Stricter threshold for production
    circuit_breaker_cooldown_seconds: 120  # Longer cooldown to prevent flapping
```

### Resource-Constrained Environment

For environments with limited resources:

```yaml
processors:
  fallback_proc_parser:
    circuit_breaker_enabled: true
    circuit_breaker_threshold_ms: 200  # More lenient threshold
    circuit_breaker_cooldown_seconds: 300  # Longer cooldown to avoid frequent checks
```

## Troubleshooting

If you're experiencing issues with the circuit breaker:

1. **Frequent circuit trips**: Consider increasing the threshold or examining system performance issues
2. **Extended periods with circuit open**: Check for persistent underlying issues in the system causing operations to be consistently slow
3. **Missing process information**: If too many operations are being blocked, consider adjusting the cache TTL to rely more on cached values and less on real-time lookups

## Conclusion

The circuit breaker pattern provides a robust mechanism for maintaining system performance and resilience even when some operations experience slowdowns. By isolating slow operations and providing fallback mechanisms, the overall telemetry pipeline can continue to function effectively.