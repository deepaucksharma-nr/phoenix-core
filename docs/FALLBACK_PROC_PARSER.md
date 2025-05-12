# Fallback Process Parser

## Overview

The Fallback Process Parser is a custom OpenTelemetry processor that enhances process metrics by adding critical attributes that might be missing from the standard hostmetrics receiver. This ensures that metrics from all processes are properly identified and can be normalized for visualization and querying, even if the default process scraper fails to collect certain attributes.

## Purpose

In some environments, the standard hostmetrics process scraper may not be able to access all the process information, especially for processes running with different permissions. The Fallback Process Parser acts as a safety net, attempting to fetch missing attributes directly from `/proc/[pid]/` for any processes where critical attributes are absent.

## Features

- **Attribute Completion**: Attempts to fetch missing `process.command_line`, `process.owner`, `process.executable.path`, and process I/O statistics from `/proc`
- **Executable Path Resolution**: Resolves executable path from `/proc/[pid]/exe` symlink
- **Username Resolution**: Converts UIDs to usernames for better readability
- **Configurable Attribute List**: Select which attributes to fetch via configuration
- **Attribute Presence Gate**: Can be configured to either warn or drop metrics for processes missing critical attributes
- **Performance Optimized**: Only processes missing attributes with the hostmetrics receiver
- **Caching**: Implements efficient caching for UID-to-username, command line, and executable path lookups
- **Detailed Metrics**: Reports throughput, latency, and error metrics for better observability

## Configuration

The Fallback Process Parser can be configured via Helm values:

```yaml
processMetrics:
  # Enable or disable process metrics collection
  enabled: true
  
  # Fallback parser configuration
  fallbackParser:
    enabled: true  # Whether to enable fallback parsing
    attributesToFetch: ["command_line", "owner", "io", "executable_path"]  # Which attributes to fetch
    cacheTTLSeconds: 300  # Cache entries TTL in seconds
  
  # Attribute presence gate for quality control
  attrPresenceGate:
    strictMode: false  # If true, drop metrics missing critical attributes; if false, just warn
    criticalAttributes: ["process.executable.name", "process.command_line"]  # Required attributes
```

### Configuration Options

| Option | Type | Description | Default |
| ------ | ---- | ----------- | ------- |
| `enabled` | boolean | Enable or disable the fallback parser | `true` |
| `attributesToFetch` | array | List of attributes to fetch from `/proc` | `["command_line", "owner", "io", "executable_path"]` |
| `strictMode` | boolean | Behavior for missing critical attributes: drop metrics if true, warn if false | `false` |
| `criticalAttributes` | array | List of attributes considered critical for process identification | `["process.executable.name", "process.command_line"]` |
| `cacheTTLSeconds` | integer | Time-to-live for cache entries in seconds | `300` (5 minutes) |

## How It Works

1. The hostmetrics receiver collects basic process metrics
2. The Fallback Process Parser examines each process resource to check for missing critical attributes
3. For PIDs missing attributes, it attempts to read the data directly from `/proc/[pid]/`
4. If strictMode is true and multiple critical attributes are still missing, the process metrics are dropped
5. If strictMode is false, a warning is logged for processes with missing attributes

### Attribute Resolution Flow

For each process with missing attributes:

1. **Executable Path Resolution**:
   - Reads the symbolic link at `/proc/[pid]/exe` to determine the full path to the executable
   - Extracts the executable name from the path if `process.executable.name` is also missing

2. **Username Resolution**:
   - Reads the UID from `/proc/[pid]/status`
   - Converts the UID to username using OS-level user lookup
   - Falls back to "uid:[number]" format if username resolution fails

3. **Command Line Resolution**:
   - Reads `/proc/[pid]/cmdline` to extract the full command line
   - Replaces null bytes with spaces for better readability

4. **I/O Statistics**:
   - Reads `/proc/[pid]/io` to extract read and write byte counts

### Caching Mechanism

The processor implements an efficient caching system:

1. Results are cached by PID (command line, executable path) or UID (username)
2. Cache entries include value, expiry time, and error information
3. Cache hits avoid expensive filesystem and OS-level lookups
4. Cache entries expire after the configured TTL (default: 5 minutes)
5. Detailed metrics track cache hit/miss rates for performance monitoring

## Process Attribute Flow

The complete process metrics flow in PTE is:

1. **hostmetrics** - Collects base metrics and attempts to get process attributes
2. **fallback_proc_parser** - Enriches missing attributes from `/proc`
3. **transform/process_normalize** - Creates `process.display_name` and `process.group`
4. **topn_process_metrics_filter** - Filters to significant processes
5. **transform/drop_pid_attr** - Removes `process.pid` to reduce cardinality

## Metrics

The Fallback Process Parser exposes the following metrics for monitoring:

| Metric | Type | Description |
| ------ | ---- | ----------- |
| `pte_fallback_parser_critical_attr_missing_total` | Counter | Count of processes missing critical attributes |
| `pte_fallback_parser_enriched_pid_total` | Counter | Count of PIDs successfully enriched |
| `pte_fallback_parser_fetch_error_total` | Counter | Count of errors when fetching attributes (with `error_type` label) |
| `pte_fallback_parser_error_by_type_total` | Counter | Detailed error counts by error type and message |
| `pte_fallback_parser_cache_hit_total` | Counter | Cache hit counts by cache type |
| `pte_fallback_parser_cache_miss_total` | Counter | Cache miss counts by cache type |
| `pte_fallback_parser_attribute_resolution_total` | Counter | Successfully resolved attributes by type and source |
| `pte_fallback_parser_fetch_duration_ms` | Histogram | Latency of attribute fetching operations in milliseconds |

## Example Use Cases

- Monitoring containerized environments where process ownership may be complex
- Ensuring command line arguments are available for service identification
- Capturing I/O metrics for disk-intensive processes
- Determining exact executable paths for security analysis and validation
- Correlating processes with their actual executable location on disk

## Related Components

- [Process Top-N Filter](TOPN_PROCESS_FILTER.md)
- [Process Metrics Cardinality Management](PROCESS_METRICS_CARDINALITY.md)

## Troubleshooting

Common issues and solutions:

1. **Permission Issues**:
   - Symptom: High error rates in `pte_fallback_parser_fetch_error_total` with "permission denied" errors
   - Solution: Ensure the collector has sufficient permissions to read from `/proc`, or disable attributes that require privileged access

2. **High Cache Miss Rates**:
   - Symptom: High values for `pte_fallback_parser_cache_miss_total` relative to hits
   - Solution: Consider increasing `cacheTTLSeconds` if process information is relatively stable in your environment

3. **Excessive Resource Usage**:
   - Symptom: High CPU usage during attribute resolution
   - Solution: Reduce the list of `attributesToFetch` to only include essential attributes

4. **Missing Executable Paths**:
   - Symptom: Executable path resolution frequently fails
   - Solution: Some processes may not have accessible executable files (e.g., kernel threads). This is normal behavior. Check if another attribute like command_line can be used instead.