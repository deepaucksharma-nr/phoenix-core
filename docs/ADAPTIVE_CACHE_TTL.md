# Adaptive Cache TTL for Fallback Process Parser

This document describes the adaptive cache TTL (Time-To-Live) implementation for the fallback process parser. This feature optimizes the caching strategy by using different expiration times for different types of process attributes.

## Overview

Process attributes have varying rates of change:

- **Executable Paths**: These rarely change during a process's lifetime
- **Usernames**: These are essentially static for a given UID
- **Command Lines**: These are static for most processes, but can change for some dynamic processes
- **I/O Statistics**: These change frequently during normal process operation

The adaptive cache TTL feature allows for configuring different TTLs for each attribute type, optimizing both cache freshness and hit rate.

## Implementation Details

### Configuration

The adaptive TTL feature is configured through the following settings:

```yaml
processors:
  fallback_proc_parser:
    enabled: true
    cache_ttl_seconds: 300                   # Default TTL for all caches (5 minutes)
    command_line_cache_ttl_seconds: 300      # TTL for command line cache (5 minutes)
    username_cache_ttl_seconds: 1800         # TTL for username cache (30 minutes)
    executable_path_cache_ttl_seconds: 3600  # TTL for executable path cache (1 hour)
```

All TTL values are specified in seconds. If specific cache TTLs are not provided, they will default to the following values:

- **command_line_cache_ttl_seconds**: Uses the general `cache_ttl_seconds` if not specified (default: 5 minutes)
- **username_cache_ttl_seconds**: 1800 seconds (30 minutes)
- **executable_path_cache_ttl_seconds**: 3600 seconds (1 hour)

### Code Changes

The implementation of adaptive TTL required the following changes:

1. **Configuration Extension**: Added new configuration parameters for each attribute type
2. **Cache Management**: Added separate TTL handling for each cache type
3. **Runtime Structure**: Maintained caching structure and performance monitoring

### Performance Benefits

The adaptive TTL approach provides the following benefits:

1. **Higher Cache Hit Rates**: Longer TTLs for stable attributes mean higher hit rates
2. **Fresher Data**: Shorter TTLs for volatile attributes mean more up-to-date data
3. **Optimized Resource Usage**: Fewer cache refreshes for stable attributes reduce CPU and I/O load

## Usage Guidelines

### Recommended TTL Values

| Attribute Type | Recommended TTL | Reasoning |
|---------------|-----------------|-----------|
| Command Line | 5-15 minutes | Generally static, but can change in some dynamic processes |
| Username | 30-60 minutes | Almost never changes for a running process |
| Executable Path | 1-24 hours | Never changes for a running process |

### Choosing Optimal TTLs

When tuning TTL values, consider:

1. **Process Lifecycle**: Short-lived processes benefit from shorter TTLs
2. **System Load**: Higher loads benefit from longer TTLs to reduce I/O pressure
3. **Data Freshness**: Critical monitoring scenarios may require fresher data

## Monitoring Cache Efficiency

To monitor the effectiveness of the adaptive TTL configuration, use the following metrics:

- `pte_fallback_parser_cache_hit_total{cache_type="executable_path"}`: Executable path cache hits
- `pte_fallback_parser_cache_miss_total{cache_type="executable_path"}`: Executable path cache misses
- `pte_fallback_parser_cache_hit_total{cache_type="username"}`: Username cache hits
- `pte_fallback_parser_cache_miss_total{cache_type="username"}`: Username cache misses
- `pte_fallback_parser_cache_hit_total{cache_type="cmdline"}`: Command line cache hits
- `pte_fallback_parser_cache_miss_total{cache_type="cmdline"}`: Command line cache misses

The cache hit ratio can be calculated as:

```
hit_ratio = hits / (hits + misses)
```

A high hit ratio (>90%) indicates effective caching. If hit ratios are lower, consider:

1. Adjusting TTL values
2. Reviewing process lifecycles on the system
3. Checking for excessive process churn

## Example Configuration

Here's an example of a configuration optimized for a system with:
- Long-running service processes
- Many short-lived batch processes
- Moderate system load

```yaml
processors:
  fallback_proc_parser:
    enabled: true
    attributes_to_fetch: ["command_line", "owner", "executable_path"]
    cache_ttl_seconds: 300                   # 5 minutes base TTL
    command_line_cache_ttl_seconds: 600      # 10 minutes for command lines
    username_cache_ttl_seconds: 3600         # 1 hour for usernames
    executable_path_cache_ttl_seconds: 7200  # 2 hours for executable paths
```

This configuration provides:
- Longer TTLs for stable attributes to maximize cache hits
- Shorter TTLs for potentially changing attributes
- Good balance between freshness and performance