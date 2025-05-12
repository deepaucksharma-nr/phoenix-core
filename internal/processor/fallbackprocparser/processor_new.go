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
}

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

	// Initialize circuit breakers
	if p.circuitBreakerEnabled {
		// Configure circuit breakers for each operation type
		p.initCircuitBreakers()
	}

	return p, nil
}

// initMetrics initializes the metrics for the processor
func (p *fallbackProcParserProcessor) initMetrics() error {
	// Initialize enriched PID counter
	_, err := p.metricsRegistry.GetOrCreateCounter(
		"pte_fallback_parser_enriched_pids_total",
		"Total number of PIDs enriched by the fallback process parser",
		metrics.UnitCount,
	)
	if err != nil {
		return fmt.Errorf("failed to create enriched PIDs counter: %w", err)
	}

	// Initialize missing critical attributes counter
	_, err = p.metricsRegistry.GetOrCreateCounter(
		"pte_fallback_parser_missing_critical_attrs_total",
		"Total number of PIDs with missing critical attributes",
		metrics.UnitCount,
	)
	if err != nil {
		return fmt.Errorf("failed to create missing critical attributes counter: %w", err)
	}

	// Add cache hit/miss counters
	_, err = p.metricsRegistry.GetOrCreateCounter(
		"pte_fallback_parser_cache_hit_total",
		"Total number of cache hits in the fallback parser",
		metrics.UnitCount,
	)
	if err != nil {
		return fmt.Errorf("failed to create cache hit counter: %w", err)
	}

	_, err = p.metricsRegistry.GetOrCreateCounter(
		"pte_fallback_parser_cache_miss_total",
		"Total number of cache misses in the fallback parser",
		metrics.UnitCount,
	)
	if err != nil {
		return fmt.Errorf("failed to create cache miss counter: %w", err)
	}

	return nil
}

// initCircuitBreakers initializes the circuit breakers for each operation type
func (p *fallbackProcParserProcessor) initCircuitBreakers() {
	// Initialize circuit breakers for expensive operations
	p.circuitBreakers.Store("exe_lookup", &circuitBreaker{
		state:             circuitStateClosed,
		failureThreshold:  p.config.CircuitBreakerFailureThreshold,
		resetTimeout:      time.Duration(p.config.CircuitBreakerResetSeconds) * time.Second,
		lastStateTransition: time.Now(),
		logger:            p.logger,
	})

	p.circuitBreakers.Store("cmdline_lookup", &circuitBreaker{
		state:             circuitStateClosed,
		failureThreshold:  p.config.CircuitBreakerFailureThreshold,
		resetTimeout:      time.Duration(p.config.CircuitBreakerResetSeconds) * time.Second,
		lastStateTransition: time.Now(),
		logger:            p.logger,
	})

	p.circuitBreakers.Store("status_lookup", &circuitBreaker{
		state:             circuitStateClosed,
		failureThreshold:  p.config.CircuitBreakerFailureThreshold,
		resetTimeout:      time.Duration(p.config.CircuitBreakerResetSeconds) * time.Second,
		lastStateTransition: time.Now(),
		logger:            p.logger,
	})
}

// processMetrics enriches metrics with process attributes from /proc filesystem
func (p *fallbackProcParserProcessor) processMetrics(ctx context.Context, metrics pmetric.Metrics) (pmetric.Metrics, error) {
	// For each metric, check if it has a process.pid attribute and enrich it
	rms := metrics.ResourceMetrics()

	for i := 0; i < rms.Len(); i++ {
		resourceMetrics := rms.At(i)
		resource := resourceMetrics.Resource()
		
		// Get process.pid attribute from resource
		pidAttr, ok := resource.Attributes().Get(attrProcessPID)
		if !ok {
			// No PID attribute, skip this resource
			continue
		}

		// Convert PID attribute to int
		pidStr := pidAttr.Str()
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			// Invalid PID format, log and skip
			p.logger.Debug("Invalid PID format",
				zap.String("pid", pidStr),
				zap.Error(err))
			continue
		}

		// Enrich resource with process attributes
		p.enrichResource(resource, pid)
	}

	return metrics, nil
}

// circuitBreaker implements a simple circuit breaker pattern
type circuitBreaker struct {
	state             string
	failureCount      atomic.Int64
	failureThreshold  int64
	resetTimeout      time.Duration
	lastStateTransition time.Time
	logger            *zap.Logger
	mu                sync.Mutex
}

// enrichResource adds process attributes to the resource
func (p *fallbackProcParserProcessor) enrichResource(resource pcommon.Resource, pid int) {
	attributes := resource.Attributes()

	// Check if critical attributes are already present
	hasCmdLine := false
	hasExeName := false
	hasExePath := false
	hasOwner := false

	// Check for existing attributes
	_, hasCmdLine = attributes.Get(attrProcessCommandLine)
	_, hasExeName = attributes.Get(attrProcessExecutableName)
	_, hasExePath = attributes.Get(attrProcessExecutablePath)
	_, hasOwner = attributes.Get(attrProcessOwner)

	// Enrichment is only needed if any attribute is missing
	if hasCmdLine && hasExeName && hasExePath && hasOwner {
		return
	}

	// Throttle warnings about missing attributes (no more than once per minute per PID)
	shouldLog := false
	pidKey := fmt.Sprintf("%d", pid)
	now := time.Now()

	if lastTime, exists := p.lastLogTime.Load(pidKey); exists {
		// Convert interface{} to time.Time
		if lastTimeVal, ok := lastTime.(time.Time); ok {
			if now.Sub(lastTimeVal) > time.Minute {
				shouldLog = true
				p.lastLogTime.Store(pidKey, now)
			}
		}
	} else {
		shouldLog = true
		p.lastLogTime.Store(pidKey, now)
	}

	// Check if any critical attribute is missing
	missingCritical := false

	// Add missing attributes
	if !hasCmdLine && p.config.EnableCommandLine {
		cmdLine := p.getCommandLine(pid)
		if cmdLine != "" {
			attributes.PutStr(attrProcessCommandLine, cmdLine)
		} else if p.config.CommandLineCritical {
			missingCritical = true
			if shouldLog {
				p.logger.Warn("Failed to get command line for process",
					zap.Int("pid", pid))
			}
		}
	}

	if !hasExePath && p.config.EnableExecutablePath {
		exePath := p.getExecutablePath(pid)
		if exePath != "" {
			attributes.PutStr(attrProcessExecutablePath, exePath)
			
			// If executable name is also missing, extract it from the path
			if !hasExeName && p.config.EnableExecutableName {
				exeName := extractExeName(exePath)
				if exeName != "" {
					attributes.PutStr(attrProcessExecutableName, exeName)
				}
			}
		} else if p.config.ExecutablePathCritical {
			missingCritical = true
			if shouldLog {
				p.logger.Warn("Failed to get executable path for process",
					zap.Int("pid", pid))
			}
		}
	}

	if !hasOwner && p.config.EnableOwner {
		owner := p.getProcessOwner(pid)
		if owner != "" {
			attributes.PutStr(attrProcessOwner, owner)
		} else if p.config.OwnerCritical {
			missingCritical = true
			if shouldLog {
				p.logger.Warn("Failed to get owner for process",
					zap.Int("pid", pid))
			}
		}
	}

	// Update metrics
	p.enrichedPIDCounter.Add(1)
	if missingCritical {
		p.criticalAttrMissingCounter.Add(1)
	}
}

// getCommandLine retrieves the command line for a process
func (p *fallbackProcParserProcessor) getCommandLine(pid int) string {
	// Check cache first with cache TTL
	if cmdLine, found := p.getCachedCommandLine(pid); found {
		// Update cache hit counter
		p.updateCacheMetric("hit", "cmdline")
		return cmdLine
	}

	// Cache miss, update metric
	p.updateCacheMetric("miss", "cmdline")

	// Check if circuit breaker is open
	if p.circuitBreakerEnabled {
		if cb, ok := p.circuitBreakers.Load("cmdline_lookup"); ok {
			if circuitBreaker, ok := cb.(*circuitBreaker); ok {
				if !circuitBreaker.allow() {
					// Circuit is open, skip the expensive operation
					return ""
				}
			}
		}
	}

	// Read command line from /proc
	cmdlinePath := fmt.Sprintf(procCmdlinePathFmt, pid)
	content, err := ioutil.ReadFile(cmdlinePath)
	
	// Handle circuit breaker result
	if p.circuitBreakerEnabled {
		if cb, ok := p.circuitBreakers.Load("cmdline_lookup"); ok {
			if circuitBreaker, ok := cb.(*circuitBreaker); ok {
				if err != nil {
					circuitBreaker.recordFailure()
				} else {
					circuitBreaker.recordSuccess()
				}
			}
		}
	}

	if err != nil {
		return ""
	}

	// Process /proc/pid/cmdline - replace null bytes with spaces
	cmdLine := strings.ReplaceAll(string(content), "\x00", " ")
	cmdLine = strings.TrimSpace(cmdLine)

	// Cache the result
	p.cacheCommandLine(pid, cmdLine)

	return cmdLine
}

// getExecutablePath retrieves the executable path for a process
func (p *fallbackProcParserProcessor) getExecutablePath(pid int) string {
	// Check cache first with cache TTL
	if exePath, found := p.getCachedExecutablePath(pid); found {
		// Update cache hit counter
		p.updateCacheMetric("hit", "executable_path")
		return exePath
	}

	// Cache miss, update metric
	p.updateCacheMetric("miss", "executable_path")

	// Check if circuit breaker is open
	if p.circuitBreakerEnabled {
		if cb, ok := p.circuitBreakers.Load("exe_lookup"); ok {
			if circuitBreaker, ok := cb.(*circuitBreaker); ok {
				if !circuitBreaker.allow() {
					// Circuit is open, skip the expensive operation
					return ""
				}
			}
		}
	}

	// Read executable path from /proc
	exePath := fmt.Sprintf(procExePathFmt, pid)
	path, err := osReadlink(exePath)

	// Handle circuit breaker result
	if p.circuitBreakerEnabled {
		if cb, ok := p.circuitBreakers.Load("exe_lookup"); ok {
			if circuitBreaker, ok := cb.(*circuitBreaker); ok {
				if err != nil {
					circuitBreaker.recordFailure()
				} else {
					circuitBreaker.recordSuccess()
				}
			}
		}
	}

	if err != nil {
		return ""
	}

	// Cache the result
	p.cacheExecutablePath(pid, path)

	return path
}

// getProcessOwner retrieves the owner username for a process
func (p *fallbackProcParserProcessor) getProcessOwner(pid int) string {
	// Check if circuit breaker is open
	if p.circuitBreakerEnabled {
		if cb, ok := p.circuitBreakers.Load("status_lookup"); ok {
			if circuitBreaker, ok := cb.(*circuitBreaker); ok {
				if !circuitBreaker.allow() {
					// Circuit is open, skip the expensive operation
					return ""
				}
			}
		}
	}

	// Read status file to find UID
	statusPath := fmt.Sprintf(procStatusPathFmt, pid)
	file, err := os.Open(statusPath)

	// Handle circuit breaker result for file open
	if p.circuitBreakerEnabled {
		if cb, ok := p.circuitBreakers.Load("status_lookup"); ok {
			if circuitBreaker, ok := cb.(*circuitBreaker); ok {
				if err != nil {
					circuitBreaker.recordFailure()
				} else {
					circuitBreaker.recordSuccess()
				}
			}
		}
	}

	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var uid string

	// Find the Uid line in /proc/pid/status
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				uid = fields[1] // Real UID is the second field
				break
			}
		}
	}

	if uid == "" {
		return ""
	}

	// Check username cache
	if username, found := p.getCachedUsername(uid); found {
		// Update cache hit counter
		p.updateCacheMetric("hit", "username")
		return username
	}

	// Cache miss, update metric
	p.updateCacheMetric("miss", "username")

	// Look up username from UID
	u, err := user.LookupId(uid)
	if err != nil {
		return ""
	}

	// Cache the username
	p.cacheUsername(uid, u.Username)

	return u.Username
}

// updateCacheMetric updates the cache hit/miss metrics
func (p *fallbackProcParserProcessor) updateCacheMetric(action, cacheType string) {
	var metricName string
	if action == "hit" {
		metricName = "pte_fallback_parser_cache_hit_total"
	} else {
		metricName = "pte_fallback_parser_cache_miss_total"
	}

	err := p.metricsRegistry.UpdateCounter(
		context.Background(),
		metricName,
		1.0,
		map[string]string{"cache_type": cacheType},
	)
	if err != nil {
		p.logger.Debug("Failed to update cache metric",
			zap.String("action", action),
			zap.String("cache_type", cacheType),
			zap.Error(err))
	}
}

// getCachedCommandLine retrieves a cached command line with TTL check
func (p *fallbackProcParserProcessor) getCachedCommandLine(pid int) (string, bool) {
	pidKey := fmt.Sprintf("%d", pid)
	value, exists := p.commandLineCache.Load(pidKey)
	if !exists {
		return "", false
	}

	// Check if the entry has a TTL
	if entry, ok := value.(cacheEntry); ok {
		if time.Since(entry.timestamp) > p.getTTLForCacheType("command_line") {
			// Entry expired
			p.commandLineCache.Delete(pidKey)
			return "", false
		}
		return entry.value, true
	}

	// For backward compatibility, handle string values
	if cmdLine, ok := value.(string); ok {
		return cmdLine, true
	}

	return "", false
}

// getCachedExecutablePath retrieves a cached executable path with TTL check
func (p *fallbackProcParserProcessor) getCachedExecutablePath(pid int) (string, bool) {
	pidKey := fmt.Sprintf("%d", pid)
	value, exists := p.executablePathCache.Load(pidKey)
	if !exists {
		return "", false
	}

	// Check if the entry has a TTL
	if entry, ok := value.(cacheEntry); ok {
		if time.Since(entry.timestamp) > p.getTTLForCacheType("executable_path") {
			// Entry expired
			p.executablePathCache.Delete(pidKey)
			return "", false
		}
		return entry.value, true
	}

	// For backward compatibility, handle string values
	if exePath, ok := value.(string); ok {
		return exePath, true
	}

	return "", false
}

// getCachedUsername retrieves a cached username with TTL check
func (p *fallbackProcParserProcessor) getCachedUsername(uid string) (string, bool) {
	value, exists := p.usernameCache.Load(uid)
	if !exists {
		return "", false
	}

	// Check if the entry has a TTL
	if entry, ok := value.(cacheEntry); ok {
		if time.Since(entry.timestamp) > p.getTTLForCacheType("username") {
			// Entry expired
			p.usernameCache.Delete(uid)
			return "", false
		}
		return entry.value, true
	}

	// For backward compatibility, handle string values
	if username, ok := value.(string); ok {
		return username, true
	}

	return "", false
}

// cacheCommandLine stores a command line in the cache with timestamp
func (p *fallbackProcParserProcessor) cacheCommandLine(pid int, cmdLine string) {
	pidKey := fmt.Sprintf("%d", pid)
	p.commandLineCache.Store(pidKey, cacheEntry{
		value:     cmdLine,
		timestamp: time.Now(),
	})
}

// cacheExecutablePath stores an executable path in the cache with timestamp
func (p *fallbackProcParserProcessor) cacheExecutablePath(pid int, exePath string) {
	pidKey := fmt.Sprintf("%d", pid)
	p.executablePathCache.Store(pidKey, cacheEntry{
		value:     exePath,
		timestamp: time.Now(),
	})
}

// cacheUsername stores a username in the cache with timestamp
func (p *fallbackProcParserProcessor) cacheUsername(uid, username string) {
	p.usernameCache.Store(uid, cacheEntry{
		value:     username,
		timestamp: time.Now(),
	})
}

// cacheEntry represents a cached value with a timestamp
type cacheEntry struct {
	value     string
	timestamp time.Time
}

// getTTLForCacheType returns the TTL for a specific cache type
func (p *fallbackProcParserProcessor) getTTLForCacheType(cacheType string) time.Duration {
	switch cacheType {
	case "command_line":
		if p.config.CommandLineCacheTTLSeconds > 0 {
			return time.Duration(p.config.CommandLineCacheTTLSeconds) * time.Second
		}
	case "executable_path":
		if p.config.ExecutablePathCacheTTLSeconds > 0 {
			return time.Duration(p.config.ExecutablePathCacheTTLSeconds) * time.Second
		}
	case "username":
		if p.config.UsernameCacheTTLSeconds > 0 {
			return time.Duration(p.config.UsernameCacheTTLSeconds) * time.Second
		}
	}
	// Use the default cache TTL
	return p.cacheTTL
}

// extractExeName extracts the executable name from a path
func extractExeName(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

// allow checks if the circuit breaker allows an operation
func (cb *circuitBreaker) allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	// Check if circuit is open
	if cb.state == circuitStateOpen {
		// Check if it's time to try again
		if time.Since(cb.lastStateTransition) > cb.resetTimeout {
			// Allow this request through as a test
			cb.state = circuitStateClosed
			cb.failureCount.Store(0)
			cb.lastStateTransition = time.Now()
			cb.logger.Info("Circuit breaker reset to closed state")
			return true
		}
		return false
	}

	return true
}

// recordSuccess records a successful operation
func (cb *circuitBreaker) recordSuccess() {
	// Reset failure count on success
	cb.failureCount.Store(0)
}

// recordFailure records a failed operation
func (cb *circuitBreaker) recordFailure() {
	currentFailures := cb.failureCount.Add(1)
	
	// Check if we need to trip the circuit breaker
	if currentFailures >= cb.failureThreshold {
		cb.mu.Lock()
		defer cb.mu.Unlock()
		
		// Double-check threshold after acquiring lock
		if cb.state == circuitStateClosed && cb.failureCount.Load() >= cb.failureThreshold {
			cb.state = circuitStateOpen
			cb.lastStateTransition = time.Now()
			cb.logger.Warn("Circuit breaker tripped to open state",
				zap.Int64("failures", currentFailures),
				zap.Int64("threshold", cb.failureThreshold))
		}
	}
}