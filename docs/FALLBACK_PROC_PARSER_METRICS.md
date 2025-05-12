# Fallback Process Parser Metrics Guide

## Overview

The Fallback Process Parser component in Phoenix Telemetry Edge (PTE) exposes a comprehensive set of metrics to monitor its performance, efficiency, and error rates. This document explains each metric, its purpose, and how to use it for monitoring and troubleshooting.

## Available Metrics

The fallback process parser exposes the following metrics:

### Core Functionality Metrics

| Metric | Type | Description | Labels |
| ------ | ---- | ----------- | ------ |
| `pte_fallback_parser_critical_attr_missing_total` | Counter | Count of processes with missing critical attributes | None |
| `pte_fallback_parser_enriched_pid_total` | Counter | Count of PIDs successfully enriched with attributes | None |

### Error Tracking Metrics

| Metric | Type | Description | Labels |
| ------ | ---- | ----------- | ------ |
| `pte_fallback_parser_fetch_error_total` | Counter | Number of errors encountered while fetching attributes | `error_type` |
| `pte_fallback_parser_error_by_type_total` | Counter | Detailed error counts categorized by type and message | `error_type`, `error_message` |

### Cache Performance Metrics

| Metric | Type | Description | Labels |
| ------ | ---- | ----------- | ------ |
| `pte_fallback_parser_cache_hit_total` | Counter | Number of cache hits by cache type | `cache_type` |
| `pte_fallback_parser_cache_miss_total` | Counter | Number of cache misses by cache type | `cache_type` |

### Attribute Resolution Metrics

| Metric | Type | Description | Labels |
| ------ | ---- | ----------- | ------ |
| `pte_fallback_parser_attribute_resolution_total` | Counter | Count of successful attribute resolutions | `attribute_type`, `source` |
| `pte_fallback_parser_fetch_duration_ms` | Histogram | Distribution of attribute fetch operation durations in milliseconds | `attribute_type` |

## Metric Labels

### Error Type Labels

The `error_type` label indicates the specific operation that failed:

- `pid_parse`: Error parsing process ID as integer
- `status_open`: Error opening the process status file
- `status_read`: Error reading from the process status file
- `uid_not_found`: UID information not found in process status
- `uid_lookup`: Error resolving UID to username
- `cmdline_read`: Error reading process command line
- `exe_readlink`: Error reading the executable path
- `io_open`: Error opening the process I/O statistics file
- `io_read`: Error reading from the I/O statistics file
- `io_parse`: Error parsing I/O statistics values

### Cache Type Labels

The `cache_type` label indicates which cache was accessed:

- `username`: Cache for UID-to-username lookups
- `cmdline`: Cache for command line lookups
- `executable_path`: Cache for executable path lookups

### Attribute Type Labels

The `attribute_type` label indicates which process attribute was being resolved:

- `username`: Process owner username
- `cmdline`: Process command line
- `executable_path`: Path to the process executable

### Source Labels

The `source` label in the `pte_fallback_parser_attribute_resolution_total` metric shows how the attribute was resolved:

- `cache`: The attribute was retrieved from cache
- `proc`: The attribute was read from the `/proc` filesystem

## Usage Examples

### Monitoring Cache Efficiency

To evaluate cache effectiveness, compare hits vs. misses:

```promql
sum(rate(pte_fallback_parser_cache_hit_total[5m])) by (cache_type) /
sum(rate(pte_fallback_parser_cache_hit_total[5m] + rate(pte_fallback_parser_cache_miss_total[5m]))) by (cache_type)
```

This gives you the cache hit ratio by cache type. Values closer to 1 indicate better cache efficiency.

### Tracking Attribute Resolution Performance

To monitor latency for different attribute lookups:

```promql
histogram_quantile(0.95, sum(rate(pte_fallback_parser_fetch_duration_ms_bucket[5m])) by (attribute_type, le))
```

This shows the 95th percentile of attribute resolution time for each attribute type.

### Error Rate Monitoring

To track error rates:

```promql
sum(rate(pte_fallback_parser_fetch_error_total[5m])) by (error_type)
```

This shows the rate of errors by type, helping identify problematic areas.

### Attribute Resolution Sources

To understand how attributes are being resolved:

```promql
sum(rate(pte_fallback_parser_attribute_resolution_total[5m])) by (attribute_type, source)
```

This breaks down attribute resolutions by type and source (cache vs. proc filesystem).

## Common Patterns and Alerting

### Recommended Alerts

1. **High Error Rate Alert**:
   ```promql
   sum(rate(pte_fallback_parser_fetch_error_total[5m])) > 10
   ```
   Triggers when error rate exceeds 10 errors per second.

2. **Low Cache Hit Ratio Alert**:
   ```promql
   sum(rate(pte_fallback_parser_cache_hit_total[5m])) by (cache_type) /
   sum(rate(pte_fallback_parser_cache_hit_total[5m] + rate(pte_fallback_parser_cache_miss_total[5m]))) by (cache_type) < 0.5
   ```
   Triggers when cache hit ratio falls below 50% for any cache type.

3. **Slow Attribute Resolution Alert**:
   ```promql
   histogram_quantile(0.95, sum(rate(pte_fallback_parser_fetch_duration_ms_bucket[5m])) by (attribute_type, le)) > 100
   ```
   Triggers when the 95th percentile of attribute resolution time exceeds 100ms.

### Dashboard Recommendations

A comprehensive dashboard should include:

1. **Cache Performance Panel**:
   - Cache hit/miss ratio over time
   - Total cache hits and misses by type

2. **Error Tracking Panel**:
   - Error rate by error type
   - Top error messages by frequency

3. **Attribute Resolution Panel**:
   - Resolution rate by attribute type
   - Resolution source distribution (cache vs. proc)

4. **Latency Panel**:
   - p50, p95, and p99 fetch durations by attribute type
   - Maximum duration trend over time

## Troubleshooting Using Metrics

### High Error Rates

If you observe high rates in `pte_fallback_parser_fetch_error_total`:

1. Check the `error_type` distribution to identify the problematic operation
2. Look at the `error_message` in `pte_fallback_parser_error_by_type_total` for specific error details
3. Common solutions include:
   - For permission errors: Adjust container/process permissions or run as privileged
   - For "file not found" errors: Check if this is expected (e.g., short-lived processes)
   - For lookup errors: Ensure the system's user database is accessible

### Poor Cache Performance

If cache hit ratios are low:

1. Check if processes are short-lived, causing frequent cache invalidation
2. Consider increasing the `cacheTTLSeconds` configuration
3. Examine if there are too many unique PIDs or UIDs overwhelming the cache

### High Latency

If attribute resolution is slow:

1. Check which attribute types have the highest latency
2. For username resolution: System user database lookup might be slow
3. For executable path: Disk or filesystem issues might be causing delays
4. Consider reducing the list of attributes to fetch if not all are needed