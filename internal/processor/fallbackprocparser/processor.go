package fallbackprocparser

import (
	"bufio"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deepaucksharma-nr/phoenix-core/internal/metrics"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/processor"
	"go.uber.org/zap"
)

// Variable to allow mocking os.Readlink in tests
var osReadlink = os.Readlink

const (
	// Process attribute names
	attrProcessPID             = "process.pid"
	attrProcessExecutableName  = "process.executable.name"
	attrProcessCommandLine     = "process.command_line"
	attrProcessOwner           = "process.owner"
	attrProcessIORead          = "process.io.read_bytes"
	attrProcessIOWrite         = "process.io.write_bytes"
	attrProcessExecutablePath  = "process.executable.path"

	// Pre-allocated proc paths for reduced allocations
	procExePathFmt      = "/proc/%d/exe"
	procCmdlinePathFmt  = "/proc/%d/cmdline"
	procStatusPathFmt   = "/proc/%d/status"
	procIOPathFmt       = "/proc/%d/io"

	// Cache settings
	defaultCacheSize    = 1000 // Initial capacity for cache maps
	defaultCacheTTL     = 300  // Default TTL in seconds

	// Circuit breaker states
	circuitStateOpen    = "open"      // Circuit is open; operations are blocked
	circuitStateClosed  = "closed"    // Circuit is closed; operations are allowed
)

// fallbackProcParserProcessor handles enriching process metrics with attributes from /proc
type fallbackProcParserProcessor struct {
	logger    *zap.Logger
	config    *Config

	// Metrics
	metricsRegistry            *metrics.MetricsRegistry
	criticalAttrMissingCounter atomic.Int64
	enrichedPIDCounter         atomic.Int64
	lastLogTime                sync.Map // PID -> time.Time for throttling warnings

	// Caches
	usernameCache       sync.Map // UID -> username cache for fast lookups
	commandLineCache    sync.Map // PID -> commandLine cache
	executablePathCache sync.Map // PID -> executablePath cache
	cacheTTL            time.Duration // Cache entry TTL

	// Circuit Breakers for slow operations
	circuitBreakerEnabled bool
	circuitBreakers       sync.Map // operation type -> circuitBreaker
)

// newProcessor creates a new fallback process parser processor.
func newProcessor(set processor.CreateSettings, config *Config) (*fallbackProcParserProcessor, error) {
	p := &fallbackProcParserProcessor{
		logger:                set.Logger,
		config:                config,
		metricsRegistry:       metrics.GetInstance(set.Logger),
		cacheTTL:              time.Duration(config.CacheTTLSeconds) * time.Second,
		circuitBreakerEnabled: config.CircuitBreakerEnabled,
	}

	// Initialize metrics
	if err := p.initMetrics(); err != nil {
		return nil, fmt.Errorf("failed to initialize metrics: %w", err)
	}

	// Add cache hit/miss counters
	if _, err := p.metricsRegistry.GetOrCreateCounter(
		"pte_fallback_parser_cache_hit_total",
		"Total number of cache hits in the fallback parser",
		metrics.UnitCount,
	); err != nil {
		return nil, fmt.Errorf("failed to initialize cache hit counter: %w", err)
	}

	if _, err := p.metricsRegistry.GetOrCreateCounter(
		"pte_fallback_parser_cache_miss_total",
		"Total number of cache misses in the fallback parser",
		metrics.UnitCount,
	); err != nil {
		return nil, fmt.Errorf("failed to initialize cache miss counter: %w", err)
	}

	// Initialize circuit breaker metrics
	if _, err := p.metricsRegistry.GetOrCreateCounter(
		"pte_fallback_parser_circuit_breaker_trip_total",
		"Total number of circuit breaker trips in the fallback parser",
		metrics.UnitCount,
	); err != nil {
		return nil, fmt.Errorf("failed to initialize circuit breaker trip counter: %w", err)
	}

	if _, err := p.metricsRegistry.GetOrCreateCounter(
		"pte_fallback_parser_circuit_breaker_state_total",
		"Total number of circuit breaker state changes in the fallback parser",
		metrics.UnitCount,
	); err != nil {
		return nil, fmt.Errorf("failed to initialize circuit breaker state counter: %w", err)
	}

	// Initialize circuit breakers for each operation type
	if p.circuitBreakerEnabled {
		cooldownPeriod := time.Duration(config.CircuitBreakerCooldownSeconds) * time.Second
		thresholdMs := float64(config.CircuitBreakerThresholdMs)

		// Create circuit breakers for each operation type
		for _, opType := range []string{"cmdline", "username", "executable_path", "io"} {
			p.circuitBreakers.Store(opType, &circuitBreaker{
				OperationType: opType,
				ThresholdMs:   thresholdMs,
				CooldownPeriod: cooldownPeriod,
				State: circuitBreakerState{
					State: circuitStateClosed,
				},
			})
		}
	}

	p.logger.Info("Fallback process parser processor created",
		zap.Bool("enabled", config.Enabled),
		zap.Strings("attributes_to_fetch", config.AttributesToFetch),
		zap.Bool("strict_mode", config.StrictMode),
		zap.Strings("critical_attributes", config.CriticalAttributes),
		zap.Duration("cache_ttl", p.cacheTTL),
		zap.Bool("executable_path_enabled", containsString(config.AttributesToFetch, "executable_path")),
		zap.Bool("circuit_breaker_enabled", p.circuitBreakerEnabled),
		zap.Int("circuit_breaker_threshold_ms", config.CircuitBreakerThresholdMs),
		zap.Int("circuit_breaker_cooldown_seconds", config.CircuitBreakerCooldownSeconds))

	return p, nil
}

// initMetrics initializes the metrics for the processor
func (p *fallbackProcParserProcessor) initMetrics() error {
	// Critical attribute missing counter
	_, err := p.metricsRegistry.GetOrCreateCounter(
		"pte_fallback_parser_critical_attr_missing_total",
		"Total number of process metrics with critical attributes missing",
		metrics.UnitProcesses,
	)
	if err != nil {
		return err
	}

	// Enriched PID counter
	_, err = p.metricsRegistry.GetOrCreateCounter(
		"pte_fallback_parser_enriched_pid_total",
		"Total number of PIDs enriched with fallback parser",
		metrics.UnitProcesses,
	)
	if err != nil {
		return err
	}

	// Attribute fetch errors counter
	_, err = p.metricsRegistry.GetOrCreateCounter(
		"pte_fallback_parser_fetch_error_total",
		"Total number of errors when fetching attributes from /proc",
		metrics.UnitCount,
	)
	if err != nil {
		return err
	}

	// Detailed error counters by type
	_, err = p.metricsRegistry.GetOrCreateCounter(
		"pte_fallback_parser_error_by_type_total",
		"Total number of errors by error type in the fallback parser",
		metrics.UnitCount,
	)
	if err != nil {
		return err
	}

	// Performance metrics for attribute resolution
	_, err = p.metricsRegistry.GetOrCreateCounter(
		"pte_fallback_parser_attribute_resolution_total",
		"Total number of attribute resolutions by type",
		metrics.UnitCount,
	)
	if err != nil {
		return err
	}

	// Attribute fetch latency histogram
	_, err = p.metricsRegistry.GetOrCreateHistogram(
		"pte_fallback_parser_fetch_duration_ms",
		"Time taken to fetch attributes in milliseconds",
		metrics.UnitMilliseconds,
	)
	if err != nil {
		return err
	}

	return nil
}

// processMetrics implements the core metrics processing logic
func (p *fallbackProcParserProcessor) processMetrics(ctx context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
	if !p.config.Enabled {
		return md, nil
	}
	
	resourceMetrics := md.ResourceMetrics()
	for i := 0; i < resourceMetrics.Len(); i++ {
		rm := resourceMetrics.At(i)
		resource := rm.Resource()
		
		// Check if this is a process metric by looking for PID
		pid := resource.Attributes().GetStr(attrProcessPID)
		if pid == "" {
			continue
		}
		
		// Check for missing critical attributes
		missingAttrs := p.checkCriticalAttributes(resource.Attributes())
		
		// If no critical attributes are missing, continue to next resource
		if len(missingAttrs) == 0 {
			continue
		}
		
		// If more than one critical attribute is missing and strict mode is enabled,
		// drop this resource's metrics if we're in strict mode
		if len(missingAttrs) >= 2 && p.config.StrictMode {
			p.criticalAttrMissingCounter.Add(1)
			p.logger.Warn("Dropping process metrics due to missing critical attributes",
				zap.String("pid", pid),
				zap.Strings("missing_attributes", missingAttrs))
			continue
		}
		
		// Try to enrich missing attributes from /proc
		enriched := p.enrichProcessAttributes(pid, resource.Attributes(), missingAttrs)
		
		// After enrichment, check again for missing critical attributes
		missingAttrs = p.checkCriticalAttributes(resource.Attributes())
		
		// If we still have too many missing critical attributes and strict mode is enabled, drop
		if len(missingAttrs) >= 2 && p.config.StrictMode {
			p.criticalAttrMissingCounter.Add(1)
			p.logger.Warn("Dropping process metrics after fallback due to missing critical attributes",
				zap.String("pid", pid),
				zap.Strings("missing_attributes", missingAttrs),
				zap.Bool("attempted_enrichment", enriched))
			continue
		}
		
		// If we still have any missing critical attributes, log a warning (throttled)
		if len(missingAttrs) > 0 {
			// Throttle warnings to avoid log spam
			if p.shouldLogWarning(pid) {
				p.logger.Warn("Process metrics missing critical attributes after fallback",
					zap.String("pid", pid),
					zap.Strings("missing_attributes", missingAttrs),
					zap.Bool("attempted_enrichment", enriched))
			}
		}
	}
	
	return md, nil
}

// checkCriticalAttributes checks if any critical attributes are missing
func (p *fallbackProcParserProcessor) checkCriticalAttributes(attrs pcommon.Map) []string {
	var missing []string
	
	for _, attr := range p.config.CriticalAttributes {
		if _, exists := attrs.Get(attr); !exists {
			missing = append(missing, attr)
		}
	}
	
	return missing
}

// enrichProcessAttributes tries to enrich process attributes from /proc
func (p *fallbackProcParserProcessor) enrichProcessAttributes(pid string, attrs pcommon.Map, missingAttrs []string) bool {
	enriched := false
	pidInt, err := strconv.Atoi(pid)
	if err != nil {
		p.logger.Debug("Failed to parse PID as integer",
			zap.String("pid", pid),
			zap.Error(err))
		p.recordErrorMetric("pid_parse", err)
		return false
	}

	// Only attempt to fetch attributes we're configured to fetch
	for _, fetchAttr := range p.config.AttributesToFetch {
		// Skip if this attribute isn't in the missing list
		shouldFetch := false
		for _, missing := range missingAttrs {
			if (fetchAttr == "command_line" && missing == attrProcessCommandLine) ||
			   (fetchAttr == "owner" && missing == attrProcessOwner) ||
			   (fetchAttr == "executable_path" && missing == attrProcessExecutablePath) {
				shouldFetch = true
				break
			}
		}

		if !shouldFetch {
			continue
		}

		// Fetch the attribute
		switch fetchAttr {
		case "command_line":
			if cmdline, err := p.fetchCommandLine(pidInt); err == nil && cmdline != "" {
				attrs.PutStr(attrProcessCommandLine, cmdline)
				enriched = true
			}
		case "owner":
			if owner, err := p.fetchOwner(pidInt); err == nil && owner != "" {
				attrs.PutStr(attrProcessOwner, owner)
				enriched = true
			}
		case "executable_path":
			if execPath, err := p.fetchExecutablePath(pidInt); err == nil && execPath != "" {
				attrs.PutStr(attrProcessExecutablePath, execPath)
				// Also extract executable name if it's not already set
				if _, exists := attrs.Get(attrProcessExecutableName); !exists {
					parts := strings.Split(execPath, "/")
					if len(parts) > 0 {
						execName := parts[len(parts)-1]
						if execName != "" {
							attrs.PutStr(attrProcessExecutableName, execName)
						}
					}
				}
				enriched = true
			}
		case "io":
			readBytes, writeBytes, err := p.fetchIO(pidInt)
			if err == nil {
				attrs.PutDouble(attrProcessIORead, readBytes)
				attrs.PutDouble(attrProcessIOWrite, writeBytes)
				enriched = true
			}
		}
	}
	
	if enriched {
		p.enrichedPIDCounter.Add(1)
	}
	
	return enriched
}

// fetchCommandLine gets the command line from /proc/[pid]/cmdline with caching
// and circuit breaker protection to prevent slow operations from impacting performance.
func (p *fallbackProcParserProcessor) fetchCommandLine(pid int) (string, error) {
	startTime := time.Now()
	pidStr := strconv.Itoa(pid)
	operationType := "cmdline"

	// Check cache first
	if entry, ok := p.commandLineCache.Load(pidStr); ok {
		cachedEntry := entry.(cacheEntry)
		// Check if entry is still valid
		if time.Now().Before(cachedEntry.Expiry) {
			// Record cache hit
			p.recordCacheHit(operationType)
			p.recordAttributeResolution(operationType, true)

			// Record performance metric (cache should be fast)
			elapsedMs := float64(time.Since(startTime).Microseconds()) / 1000.0
			p.recordFetchDuration(operationType, elapsedMs)

			if cachedEntry.HasError {
				return "", fmt.Errorf(cachedEntry.ErrorMsg)
			}
			return cachedEntry.Value, nil
		}
		// Cache entry expired, remove it
		p.commandLineCache.Delete(pidStr)
	}

	// Record cache miss
	p.recordCacheMiss(operationType)

	// Check if operation is allowed by circuit breaker
	if !p.allowOperation(operationType) {
		p.logger.Debug("Circuit breaker prevented command line fetch operation",
			zap.Int("pid", pid))
		return "", fmt.Errorf("operation blocked by circuit breaker")
	}

	// Perform the actual lookup
	cmdlineFile := fmt.Sprintf("/proc/%d/cmdline", pid)
	data, err := ioutil.ReadFile(cmdlineFile)

	// Record performance metric for the file read
	elapsedMs := float64(time.Since(startTime).Microseconds()) / 1000.0
	p.recordFetchDuration(operationType, elapsedMs)

	// Update the circuit breaker state based on operation result
	p.recordOperationResult(operationType, elapsedMs, err == nil)

	// Cache the result (even errors) to prevent repeated lookups
	ttl := time.Duration(p.config.CommandLineCacheTTLSeconds) * time.Second
	if err != nil {
		// Record the error for monitoring
		p.recordErrorMetric("cmdline_read", err)

		p.commandLineCache.Store(pidStr, cacheEntry{
			Value:    "",
			Expiry:   time.Now().Add(ttl),
			HasError: true,
			ErrorMsg: err.Error(),
		})

		return "", fmt.Errorf("failed to read cmdline for PID %d: %w", pid, err)
	}

	// Record successful resolution
	p.recordAttributeResolution(operationType, false)

	// Replace null bytes with spaces for better readability
	cmdline := strings.ReplaceAll(string(data), "\x00", " ")
	cmdline = strings.TrimSpace(cmdline)

	// Store in cache
	p.commandLineCache.Store(pidStr, cacheEntry{
		Value:    cmdline,
		Expiry:   time.Now().Add(ttl),
		HasError: false,
	})

	return cmdline, nil
}

// fetchOwner gets the process owner from /proc/[pid]/status and resolves UID to username
func (p *fallbackProcParserProcessor) fetchOwner(pid int) (string, error) {
	statusFile := fmt.Sprintf("/proc/%d/status", pid)
	file, err := os.Open(statusFile)
	if err != nil {
		p.recordErrorMetric("status_open", err)
		return "", fmt.Errorf("failed to open status file for PID %d: %w", pid, err)
	}
	defer file.Close()
	
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Uid:") {
			uidParts := strings.Fields(line)
			if len(uidParts) >= 2 {
				// Get real UID (the first one after "Uid:")
				uid := uidParts[1]
				
				// Convert UID to username
				username, err := p.resolveUIDToUsername(uid)
				if err != nil {
					// In case of error, fall back to just using the UID
					p.logger.Debug("Failed to resolve UID to username", 
						zap.String("uid", uid), 
						zap.Error(err))
					return "uid:" + uid, nil
				}
				
				return username, nil
			}
		}
	}
	
	if err := scanner.Err(); err != nil {
		p.recordErrorMetric("status_read", err)
		return "", fmt.Errorf("error reading status file for PID %d: %w", pid, err)
	}
	
	p.recordErrorMetric("uid_not_found", nil)
	return "", fmt.Errorf("owner information not found in status file for PID %d", pid)
}

// cacheEntry represents a cached value with an expiry time and error information.
// This struct is used to store the results of attribute lookups in memory caches,
// including both successful values and error states, to prevent repeated expensive lookups.
//
// Fields:
//   - Value: The cached value (e.g., username, command line, executable path)
//   - Expiry: The time when this cache entry becomes invalid
//   - HasError: Whether this entry represents an error state
//   - ErrorMsg: The error message if HasError is true
type cacheEntry struct {
	Value      string
	Expiry     time.Time
	HasError   bool
	ErrorMsg   string
}

// circuitBreakerState represents the current state of a circuit breaker.
// Circuit breakers are used to prevent repeated slow operations by "opening"
// the circuit and quickly failing when operations are too slow or resource-intensive.
//
// Fields:
//   - State: The current state ("open" or "closed")
//   - UntilTime: The time until which the circuit will remain open
//   - SlowOperationCount: A count of consecutive slow operations before opening
type circuitBreakerState struct {
	State              string
	UntilTime          time.Time
	SlowOperationCount int
}

// circuitBreaker implements a simple circuit breaker pattern to prevent
// slow operations from affecting overall system performance.
// When an operation is too slow, the circuit "opens" and rejects requests
// for a cooldown period, then transitions to "closed" to allow operations again.
//
// Fields:
//   - OperationType: The type of operation this circuit breaker protects
//   - ThresholdMs: The maximum allowed operation time in ms before considering it slow
//   - CooldownPeriod: The duration the circuit remains open after being tripped
//   - State: The current state of the circuit breaker
//   - Lock: Mutex to protect concurrent access to the state
type circuitBreaker struct {
	OperationType string
	ThresholdMs   float64
	CooldownPeriod time.Duration
	State         circuitBreakerState
	Lock          sync.RWMutex
}

// resolveUIDToUsername converts a numeric user ID to a human-readable username with caching.
// It uses the OS's user.LookupId function to perform the actual lookup and caches the
// results to improve performance for repeated lookups. The function also tracks metrics
// for both successful and failed lookups, cache efficiency, and latency.
//
// This function implements circuit breaker protection to prevent slow username lookups
// from impacting overall telemetry processing performance.
//
// Parameters:
//   - uid: The user ID to resolve to a username
//
// Returns:
//   - string: The resolved username, or "uid:[number]" if resolution fails
//   - error: Error encountered during the resolution, or nil if successful
func (p *fallbackProcParserProcessor) resolveUIDToUsername(uid string) (string, error) {
	startTime := time.Now()
	operationType := "username"

	// If it's an empty UID, just return empty
	if uid == "" {
		return "", fmt.Errorf("empty UID provided")
	}

	// Check cache first
	if entry, ok := p.usernameCache.Load(uid); ok {
		cachedEntry := entry.(cacheEntry)
		// Check if entry is still valid
		if time.Now().Before(cachedEntry.Expiry) {
			// Record cache hit
			p.recordCacheHit(operationType)
			p.recordAttributeResolution(operationType, true)

			// Record performance metric (cache should be fast)
			elapsedMs := float64(time.Since(startTime).Microseconds()) / 1000.0
			p.recordFetchDuration(operationType, elapsedMs)

			if cachedEntry.HasError {
				return "", fmt.Errorf(cachedEntry.ErrorMsg)
			}
			return cachedEntry.Value, nil
		}
		// Cache entry expired, remove it
		p.usernameCache.Delete(uid)
	}

	// Record cache miss
	p.recordCacheMiss(operationType)

	// Check if operation is allowed by circuit breaker
	if !p.allowOperation(operationType) {
		p.logger.Debug("Circuit breaker prevented username resolution operation",
			zap.String("uid", uid))
		return "uid:" + uid, nil // Return a fallback instead of error
	}

	// Perform the actual lookup
	u, err := user.LookupId(uid)

	// Record performance metric for the user lookup
	elapsedMs := float64(time.Since(startTime).Microseconds()) / 1000.0
	p.recordFetchDuration(operationType, elapsedMs)

	// Update the circuit breaker state based on operation result
	p.recordOperationResult(operationType, elapsedMs, err == nil)

	// Cache the result (even errors) to prevent repeated lookups
	ttl := time.Duration(p.config.UsernameCacheTTLSeconds) * time.Second
	if err != nil {
		p.recordErrorMetric("uid_lookup", err)

		p.usernameCache.Store(uid, cacheEntry{
			Value:    "uid:" + uid, // Store a usable fallback value
			Expiry:   time.Now().Add(ttl),
			HasError: true,
			ErrorMsg: err.Error(),
		})
		return "uid:" + uid, nil // Return a fallback instead of error
	}

	// Record successful resolution
	p.recordAttributeResolution(operationType, false)

	// Handle empty username edge case
	username := u.Username
	if username == "" {
		username = "uid:" + uid
	}

	// Store in cache
	p.usernameCache.Store(uid, cacheEntry{
		Value:    username,
		Expiry:   time.Now().Add(ttl),
		HasError: false,
	})

	return username, nil
}

// fetchExecutablePath gets the executable path from /proc/[pid]/exe with caching.
// It resolves the symlink at /proc/[pid]/exe to get the full path to the executable file.
// Results are cached to improve performance for repeated lookups of the same PID.
// The function also records performance metrics for monitoring cache efficiency and latency.
//
// This function implements circuit breaker protection to prevent slow symlink resolution
// operations from impacting overall telemetry processing performance.
//
// Parameters:
//   - pid: The process ID to fetch the executable path for
//
// Returns:
//   - string: The resolved executable path, or empty string if not found
//   - error: Error encountered during the operation, or nil if successful
func (p *fallbackProcParserProcessor) fetchExecutablePath(pid int) (string, error) {
	startTime := time.Now()
	pidStr := strconv.Itoa(pid)
	operationType := "executable_path"

	// Check cache first
	if entry, ok := p.executablePathCache.Load(pidStr); ok {
		cachedEntry := entry.(cacheEntry)
		// Check if entry is still valid
		if time.Now().Before(cachedEntry.Expiry) {
			// Record cache hit
			p.recordCacheHit(operationType)
			p.recordAttributeResolution(operationType, true)

			// Record performance metric (cache should be fast)
			elapsedMs := float64(time.Since(startTime).Microseconds()) / 1000.0
			p.recordFetchDuration(operationType, elapsedMs)

			if cachedEntry.HasError {
				return "", fmt.Errorf(cachedEntry.ErrorMsg)
			}
			return cachedEntry.Value, nil
		}
		// Cache entry expired, remove it
		p.executablePathCache.Delete(pidStr)
	}

	// Record cache miss
	p.recordCacheMiss(operationType)

	// Check if operation is allowed by circuit breaker
	if !p.allowOperation(operationType) {
		p.logger.Debug("Circuit breaker prevented executable path resolution",
			zap.Int("pid", pid))
		return "", fmt.Errorf("operation blocked by circuit breaker")
	}

	// The /proc/[pid]/exe is a symlink to the executable file
	exePath := fmt.Sprintf("/proc/%d/exe", pid)
	realPath, err := osReadlink(exePath)

	// Record performance metric for the proc filesystem lookup
	elapsedMs := float64(time.Since(startTime).Microseconds()) / 1000.0
	p.recordFetchDuration(operationType, elapsedMs)

	// Update the circuit breaker state based on operation result
	p.recordOperationResult(operationType, elapsedMs, err == nil)

	// Cache the result (even errors) to prevent repeated lookups
	ttl := time.Duration(p.config.ExecutablePathCacheTTLSeconds) * time.Second
	if err != nil {
		// Record the error for monitoring
		p.recordErrorMetric("exe_readlink", err)

		p.executablePathCache.Store(pidStr, cacheEntry{
			Value:    "",
			Expiry:   time.Now().Add(ttl),
			HasError: true,
			ErrorMsg: err.Error(),
		})

		return "", fmt.Errorf("failed to read executable path for PID %d: %w", pid, err)
	}

	// Record successful resolution
	p.recordAttributeResolution(operationType, false)

	// Store in cache
	p.executablePathCache.Store(pidStr, cacheEntry{
		Value:    realPath,
		Expiry:   time.Now().Add(ttl),
		HasError: false,
	})

	return realPath, nil
}

// fetchIO gets the IO statistics from /proc/[pid]/io with circuit breaker protection
// to prevent slow I/O operations from impacting overall telemetry processing performance.
func (p *fallbackProcParserProcessor) fetchIO(pid int) (float64, float64, error) {
	startTime := time.Now()
	operationType := "io"

	// Check if operation is allowed by circuit breaker
	if !p.allowOperation(operationType) {
		p.logger.Debug("Circuit breaker prevented IO statistics fetch operation",
			zap.Int("pid", pid))
		return 0, 0, fmt.Errorf("operation blocked by circuit breaker")
	}

	ioFile := fmt.Sprintf("/proc/%d/io", pid)
	file, err := os.Open(ioFile)
	if err != nil {
		p.recordErrorMetric("io_open", err)

		// Record operation result for circuit breaker
		elapsedMs := float64(time.Since(startTime).Microseconds()) / 1000.0
		p.recordFetchDuration(operationType, elapsedMs)
		p.recordOperationResult(operationType, elapsedMs, false)

		return 0, 0, fmt.Errorf("failed to open IO file for PID %d: %w", pid, err)
	}
	defer file.Close()

	var readBytes, writeBytes float64

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "read_bytes:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				bytes, err := strconv.ParseFloat(parts[1], 64)
				if err == nil {
					readBytes = bytes
				} else {
					p.recordErrorMetric("io_parse", err)
				}
			}
		} else if strings.HasPrefix(line, "write_bytes:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				bytes, err := strconv.ParseFloat(parts[1], 64)
				if err == nil {
					writeBytes = bytes
				} else {
					p.recordErrorMetric("io_parse", err)
				}
			}
		}
	}

	scanErr := scanner.Err()

	// Record performance metric for the IO operation
	elapsedMs := float64(time.Since(startTime).Microseconds()) / 1000.0
	p.recordFetchDuration(operationType, elapsedMs)
	p.recordOperationResult(operationType, elapsedMs, scanErr == nil)

	if scanErr != nil {
		p.recordErrorMetric("io_read", scanErr)
		return 0, 0, fmt.Errorf("error reading IO file for PID %d: %w", pid, scanErr)
	}

	return readBytes, writeBytes, nil
}

// shouldLogWarning determines if we should log a warning for this PID based on throttling
func (p *fallbackProcParserProcessor) shouldLogWarning(pid string) bool {
	now := time.Now()
	
	// Check if we've logged recently for this PID
	if lastTime, ok := p.lastLogTime.LoadOrStore(pid, now); ok {
		// Only log once per minute per PID
		if now.Sub(lastTime.(time.Time)) < time.Minute {
			return false
		}
		// Update the last log time
		p.lastLogTime.Store(pid, now)
	}
	
	return true
}

// recordCacheHit records a cache hit metric for monitoring cache effectiveness.
// This function updates the pte_fallback_parser_cache_hit_total counter with appropriate labels
// to track which types of caches are performing well and how often the caches are used.
//
// Parameters:
//   - cacheType: The type of cache that had a hit (e.g., "username", "cmdline", "executable_path")
func (p *fallbackProcParserProcessor) recordCacheHit(cacheType string) {
	err := p.metricsRegistry.UpdateCounter(
		context.Background(),
		"pte_fallback_parser_cache_hit_total",
		1.0,
		map[string]string{"cache_type": cacheType},
	)
	if err != nil {
		p.logger.Debug("Failed to update cache hit metric", zap.Error(err))
	}
}

// recordCacheMiss records a cache miss metric for monitoring cache effectiveness.
// This function updates the pte_fallback_parser_cache_miss_total counter with appropriate labels
// to track which types of caches are having misses, indicating potential areas for optimization
// or configuration adjustments (e.g., longer TTL for relatively stable data).
//
// Parameters:
//   - cacheType: The type of cache that had a miss (e.g., "username", "cmdline", "executable_path")
func (p *fallbackProcParserProcessor) recordCacheMiss(cacheType string) {
	err := p.metricsRegistry.UpdateCounter(
		context.Background(),
		"pte_fallback_parser_cache_miss_total",
		1.0,
		map[string]string{"cache_type": cacheType},
	)
	if err != nil {
		p.logger.Debug("Failed to update cache miss metric", zap.Error(err))
	}
}

// recordErrorMetric records an error by type and message for detailed monitoring.
// This function updates two counter metrics:
// 1. A general error counter with just the error type for high-level monitoring
// 2. A detailed error counter with both type and message for in-depth analysis
//
// The error message is extracted and normalized to avoid high cardinality in metrics
// while still providing useful error categorization.
//
// Parameters:
//   - errorType: The category of error (e.g., "uid_lookup", "cmdline_read", "exe_readlink")
//   - err: The actual error object, or nil if no specific error (just counting an error event)
func (p *fallbackProcParserProcessor) recordErrorMetric(errorType string, err error) {
	// Update general error counter
	updateErr := p.metricsRegistry.UpdateCounter(
		context.Background(),
		"pte_fallback_parser_fetch_error_total",
		1.0,
		map[string]string{"error_type": errorType},
	)
	if updateErr != nil {
		p.logger.Debug("Failed to update error counter", zap.Error(updateErr))
	}
	
	// Update detailed error counter
	errorMessage := "unknown"
	if err != nil {
		// Extract the base error message without the path or context
		// This helps group similar errors together
		errorParts := strings.Split(err.Error(), ":")
		if len(errorParts) > 0 {
			errorMessage = strings.TrimSpace(errorParts[len(errorParts)-1])
		}
	}
	
	updateErr = p.metricsRegistry.UpdateCounter(
		context.Background(),
		"pte_fallback_parser_error_by_type_total",
		1.0,
		map[string]string{
			"error_type":    errorType,
			"error_message": errorMessage,
		},
	)
	if updateErr != nil {
		p.logger.Debug("Failed to update detailed error counter", zap.Error(updateErr))
	}
}

// Start implements component.Component
func (p *fallbackProcParserProcessor) Start(_ context.Context, _ component.Host) error {
	if !p.config.Enabled {
		p.logger.Info("Fallback process parser is disabled")
	} else {
		// Create a string map of features for more informative logging
		features := map[string]bool{
			"command_line":    containsString(p.config.AttributesToFetch, "command_line"),
			"owner":           containsString(p.config.AttributesToFetch, "owner"),
			"io":              containsString(p.config.AttributesToFetch, "io"),
			"executable_path": containsString(p.config.AttributesToFetch, "executable_path"),
		}

		p.logger.Info("Fallback process parser started",
			zap.Strings("attributes_to_fetch", p.config.AttributesToFetch),
			zap.Bool("strict_mode", p.config.StrictMode),
			zap.Bool("command_line_enabled", features["command_line"]),
			zap.Bool("owner_enabled", features["owner"]),
			zap.Bool("io_enabled", features["io"]),
			zap.Bool("executable_path_enabled", features["executable_path"]))
	}
	return nil
}

// Shutdown implements component.Component
func (p *fallbackProcParserProcessor) Shutdown(_ context.Context) error {
	return nil
}

// recordAttributeResolution records a successful attribute resolution in metrics.
// This function tracks which attributes were successfully resolved and from which source
// (cache or direct filesystem read) to help monitor the processor's efficiency.
//
// Parameters:
//   - attrType: The type of attribute resolved (e.g., "username", "cmdline", "executable_path")
//   - cacheHit: Whether the resolution was satisfied from cache (true) or from /proc (false)
func (p *fallbackProcParserProcessor) recordAttributeResolution(attrType string, cacheHit bool) {
	source := "cache"
	if !cacheHit {
		source = "proc"
	}

	err := p.metricsRegistry.UpdateCounter(
		context.Background(),
		"pte_fallback_parser_attribute_resolution_total",
		1.0,
		map[string]string{
			"attribute_type": attrType,
			"source":         source,
		},
	)
	if err != nil {
		p.logger.Debug("Failed to update attribute resolution metric", zap.Error(err))
	}
}

// recordFetchDuration records the time taken to fetch an attribute as a histogram metric.
// This function tracks the latency of attribute fetching operations to help identify
// performance bottlenecks and monitor the impact of caching.
//
// Parameters:
//   - attrType: The type of attribute being fetched (e.g., "username", "cmdline", "executable_path")
//   - durationMs: The duration of the fetch operation in milliseconds
func (p *fallbackProcParserProcessor) recordFetchDuration(attrType string, durationMs float64) {
	err := p.metricsRegistry.UpdateHistogram(
		context.Background(),
		"pte_fallback_parser_fetch_duration_ms",
		durationMs,
		map[string]string{
			"attribute_type": attrType,
		},
	)
	if err != nil {
		p.logger.Debug("Failed to update fetch duration metric", zap.Error(err))
	}
}

// containsString checks if a string is present in a slice.
// This is a utility function used to verify if a specific attribute is configured
// to be fetched or if it's considered a critical attribute.
//
// Parameters:
//   - slice: The slice of strings to search in
//   - target: The string to search for
//
// Returns:
//   - bool: True if the target string is found in the slice, false otherwise
func containsString(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

// allowOperation checks if an operation is allowed by the circuit breaker.
// If the circuit breaker is open, the operation is not allowed.
// This helps prevent slow operations from affecting overall system performance.
//
// Parameters:
//   - operationType: The type of operation to check (e.g., "cmdline", "username")
//
// Returns:
//   - bool: True if operation is allowed, false if circuit is open and operation is denied
func (p *fallbackProcParserProcessor) allowOperation(operationType string) bool {
	// If circuit breakers are disabled, always allow operation
	if !p.circuitBreakerEnabled {
		return true
	}

	// Get the circuit breaker for this operation type
	if cbObj, exists := p.circuitBreakers.Load(operationType); exists {
		cb := cbObj.(*circuitBreaker)
		cb.Lock.RLock()
		defer cb.Lock.RUnlock()

		// If the circuit is open, check if it's time to try again
		if cb.State.State == circuitStateOpen {
			// If we're still within the open period, deny the operation
			if time.Now().Before(cb.State.UntilTime) {
				return false
			}

			// Otherwise, we'll allow this operation as a test
			// The circuit's state will be updated by recordOperationResult
			return true
		}
	}

	// By default, allow the operation
	return true
}

// recordOperationResult records the result of an operation and updates the circuit breaker state.
// If the operation is too slow and exceeds the threshold, the circuit may be opened.
//
// Parameters:
//   - operationType: The type of operation (e.g., "cmdline", "username")
//   - durationMs: The duration of the operation in milliseconds
//   - success: Whether the operation was successful
func (p *fallbackProcParserProcessor) recordOperationResult(operationType string, durationMs float64, success bool) {
	// If circuit breakers are disabled, don't track operation results
	if !p.circuitBreakerEnabled {
		return
	}

	// Get the circuit breaker for this operation type
	cbObj, exists := p.circuitBreakers.Load(operationType)
	if !exists {
		// Should never happen if initialized properly
		p.logger.Warn("Circuit breaker not found for operation type",
			zap.String("operation_type", operationType))
		return
	}

	cb := cbObj.(*circuitBreaker)
	cb.Lock.Lock()
	defer cb.Lock.Unlock()

	// Update circuit breaker state based on operation result
	if !success || durationMs > cb.ThresholdMs {
		// Operation was slow (exceeded threshold) or failed
		cb.State.SlowOperationCount++

		// If we've had 3 consecutive slow operations, open the circuit
		if cb.State.SlowOperationCount >= 3 && cb.State.State == circuitStateClosed {
			// Open the circuit for the cooldown period
			cb.State.State = circuitStateOpen
			cb.State.UntilTime = time.Now().Add(cb.CooldownPeriod)

			// Record circuit breaker trip
			p.recordCircuitBreakerTrip(operationType, durationMs)

			p.logger.Warn("Circuit breaker tripped for operation type",
				zap.String("operation_type", operationType),
				zap.Float64("duration_ms", durationMs),
				zap.Float64("threshold_ms", cb.ThresholdMs),
				zap.Duration("cooldown_period", cb.CooldownPeriod))
		}
	} else {
		// Operation was successful and within threshold time
		// If we were in an open state and this succeeds, close the circuit
		if cb.State.State == circuitStateOpen {
			cb.State.State = circuitStateClosed
			cb.State.SlowOperationCount = 0

			// Record circuit breaker state change
			p.recordCircuitBreakerStateChange(operationType, circuitStateClosed)

			p.logger.Info("Circuit breaker closed for operation type after successful test operation",
				zap.String("operation_type", operationType))
		} else if cb.State.SlowOperationCount > 0 {
			// Reset slow operation counter if we have a fast operation
			cb.State.SlowOperationCount = 0
		}
	}
}

// recordCircuitBreakerTrip records a circuit breaker trip in metrics.
// This helps monitor which operations are frequently triggering circuit breakers.
//
// Parameters:
//   - operationType: The type of operation that triggered the circuit breaker
//   - durationMs: The duration of the operation that triggered the circuit breaker
func (p *fallbackProcParserProcessor) recordCircuitBreakerTrip(operationType string, durationMs float64) {
	err := p.metricsRegistry.UpdateCounter(
		context.Background(),
		"pte_fallback_parser_circuit_breaker_trip_total",
		1.0,
		map[string]string{
			"operation_type": operationType,
			"duration_tier": getDurationTier(durationMs),
		},
	)
	if err != nil {
		p.logger.Debug("Failed to update circuit breaker trip metric", zap.Error(err))
	}
}

// recordCircuitBreakerStateChange records a change in circuit breaker state in metrics.
// This helps track how often circuit breakers are cycling between open and closed states.
//
// Parameters:
//   - operationType: The type of operation whose circuit breaker changed state
//   - state: The new state of the circuit breaker ("open" or "closed")
func (p *fallbackProcParserProcessor) recordCircuitBreakerStateChange(operationType string, state string) {
	err := p.metricsRegistry.UpdateCounter(
		context.Background(),
		"pte_fallback_parser_circuit_breaker_state_total",
		1.0,
		map[string]string{
			"operation_type": operationType,
			"state": state,
		},
	)
	if err != nil {
		p.logger.Debug("Failed to update circuit breaker state metric", zap.Error(err))
	}
}

// getDurationTier returns a tier label for a duration to reduce cardinality in metrics.
// This categorizes durations into preset tiers (e.g., "<100ms", "100-500ms", ">1s").
//
// Parameters:
//   - durationMs: The duration in milliseconds to categorize
//
// Returns:
//   - string: A string representation of the duration tier
func getDurationTier(durationMs float64) string {
	switch {
	case durationMs < 100:
		return "<100ms"
	case durationMs < 500:
		return "100-500ms"
	case durationMs < 1000:
		return "500-1000ms"
	default:
		return ">1s"
	}
}