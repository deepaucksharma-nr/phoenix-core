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
	// Process attribute names (unchanged)
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
}

// newProcessor creates a new fallback process parser processor.
// Updated to include optimized paths and cache initialization
func newOptimizedProcessor(set processor.CreateSettings, config *Config) (*fallbackProcParserProcessor, error) {
	p := &fallbackProcParserProcessor{
		logger:          set.Logger,
		config:          config,
		metricsRegistry: metrics.GetInstance(set.Logger),
		cacheTTL:        time.Duration(config.CacheTTLSeconds) * time.Second,
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
	
	p.logger.Info("Fallback process parser processor created",
		zap.Bool("enabled", config.Enabled),
		zap.Strings("attributes_to_fetch", config.AttributesToFetch),
		zap.Bool("strict_mode", config.StrictMode),
		zap.Strings("critical_attributes", config.CriticalAttributes),
		zap.Duration("cache_ttl", p.cacheTTL),
		zap.Bool("executable_path_enabled", containsString(config.AttributesToFetch, "executable_path")))
	
	return p, nil
}

// Optimized version of fetchExecutablePath that reduces allocations
func (p *fallbackProcParserProcessor) fetchExecutablePathOptimized(pid int) (string, error) {
	startTime := time.Now()
	pidStr := strconv.Itoa(pid)
	
	// Check cache first - unchanged from original
	if entry, ok := p.executablePathCache.Load(pidStr); ok {
		cachedEntry := entry.(cacheEntry)
		if time.Now().Before(cachedEntry.Expiry) {
			p.recordCacheHit("executable_path")
			p.recordAttributeResolution("executable_path", true)
			
			elapsedMs := float64(time.Since(startTime).Microseconds()) / 1000.0
			p.recordFetchDuration("executable_path", elapsedMs)
			
			if cachedEntry.HasError {
				return "", fmt.Errorf(cachedEntry.ErrorMsg)
			}
			return cachedEntry.Value, nil
		}
		p.executablePathCache.Delete(pidStr)
	}
	
	// Record cache miss
	p.recordCacheMiss("executable_path")
	
	// Use the pre-allocated format string to reduce allocations
	exePath := fmt.Sprintf(procExePathFmt, pid)
	realPath, err := osReadlink(exePath)
	
	// Record performance metric
	elapsedMs := float64(time.Since(startTime).Microseconds()) / 1000.0
	p.recordFetchDuration("executable_path", elapsedMs)
	
	// Cache the result (even errors) to prevent repeated lookups
	if err != nil {
		p.recordErrorMetric("exe_readlink", err)
		
		p.executablePathCache.Store(pidStr, cacheEntry{
			Value:    "",
			Expiry:   time.Now().Add(p.cacheTTL),
			HasError: true,
			ErrorMsg: err.Error(),
		})
		
		return "", fmt.Errorf("failed to read executable path for PID %d: %w", pid, err)
	}
	
	// Record successful resolution
	p.recordAttributeResolution("executable_path", false)
	
	// Store in cache
	p.executablePathCache.Store(pidStr, cacheEntry{
		Value:    realPath,
		Expiry:   time.Now().Add(p.cacheTTL),
		HasError: false,
	})
	
	return realPath, nil
}

// Optimized version of fetchCommandLine that reduces allocations
func (p *fallbackProcParserProcessor) fetchCommandLineOptimized(pid int) (string, error) {
	startTime := time.Now()
	pidStr := strconv.Itoa(pid)
	
	// Check cache first
	if entry, ok := p.commandLineCache.Load(pidStr); ok {
		cachedEntry := entry.(cacheEntry)
		if time.Now().Before(cachedEntry.Expiry) {
			p.recordCacheHit("cmdline")
			p.recordAttributeResolution("cmdline", true)
			
			elapsedMs := float64(time.Since(startTime).Microseconds()) / 1000.0
			p.recordFetchDuration("cmdline", elapsedMs)
			
			if cachedEntry.HasError {
				return "", fmt.Errorf(cachedEntry.ErrorMsg)
			}
			return cachedEntry.Value, nil
		}
		p.commandLineCache.Delete(pidStr)
	}
	
	// Record cache miss
	p.recordCacheMiss("cmdline")
	
	// Use the pre-allocated format string to reduce allocations
	cmdlineFile := fmt.Sprintf(procCmdlinePathFmt, pid)
	data, err := ioutil.ReadFile(cmdlineFile)
	
	// Record performance metric
	elapsedMs := float64(time.Since(startTime).Microseconds()) / 1000.0
	p.recordFetchDuration("cmdline", elapsedMs)
	
	// Cache the result (even errors) to prevent repeated lookups
	if err != nil {
		p.recordErrorMetric("cmdline_read", err)
		
		p.commandLineCache.Store(pidStr, cacheEntry{
			Value:    "",
			Expiry:   time.Now().Add(p.cacheTTL),
			HasError: true,
			ErrorMsg: err.Error(),
		})
		
		return "", fmt.Errorf("failed to read cmdline for PID %d: %w", pid, err)
	}
	
	// Record successful resolution
	p.recordAttributeResolution("cmdline", false)
	
	// Replace null bytes with spaces for better readability
	// Preallocate the string builder for better performance
	var sb strings.Builder
	sb.Grow(len(data)) // Preallocate capacity
	
	for i, b := range data {
		if b == 0 {
			sb.WriteByte(' ')
		} else {
			sb.WriteByte(b)
		}
	}
	
	cmdline := strings.TrimSpace(sb.String())
	
	// Store in cache
	p.commandLineCache.Store(pidStr, cacheEntry{
		Value:    cmdline,
		Expiry:   time.Now().Add(p.cacheTTL),
		HasError: false,
	})
	
	return cmdline, nil
}

// Optimized fetchOwner implementation with reduced allocations
func (p *fallbackProcParserProcessor) fetchOwnerOptimized(pid int) (string, error) {
	statusFile := fmt.Sprintf(procStatusPathFmt, pid)
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

// Optimized fetchIO implementation with reduced allocations
func (p *fallbackProcParserProcessor) fetchIOOptimized(pid int) (float64, float64, error) {
	ioFile := fmt.Sprintf(procIOPathFmt, pid)
	file, err := os.Open(ioFile)
	if err != nil {
		p.recordErrorMetric("io_open", err)
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
	
	if err := scanner.Err(); err != nil {
		p.recordErrorMetric("io_read", err)
		return 0, 0, fmt.Errorf("error reading IO file for PID %d: %w", pid, err)
	}
	
	return readBytes, writeBytes, nil
}

// Optimized enrichProcessAttributes with better cache key handling
func (p *fallbackProcParserProcessor) enrichProcessAttributesOptimized(pid string, attrs pcommon.Map, missingAttrs []string) bool {
	enriched := false
	pidInt, err := strconv.Atoi(pid)
	if err != nil {
		p.logger.Debug("Failed to parse PID as integer", 
			zap.String("pid", pid), 
			zap.Error(err))
		p.recordErrorMetric("pid_parse", err)
		return false
	}
	
	// Create a set of missing attributes for faster lookups
	missingSet := make(map[string]bool, len(missingAttrs))
	for _, attr := range missingAttrs {
		missingSet[attr] = true
	}
	
	// Only attempt to fetch attributes we're configured to fetch
	for _, fetchAttr := range p.config.AttributesToFetch {
		// Check if this attribute needs to be fetched
		var shouldFetch bool
		switch fetchAttr {
		case "command_line":
			shouldFetch = missingSet[attrProcessCommandLine]
		case "owner":
			shouldFetch = missingSet[attrProcessOwner]
		case "executable_path":
			shouldFetch = missingSet[attrProcessExecutablePath]
		case "io":
			shouldFetch = missingSet[attrProcessIORead] || missingSet[attrProcessIOWrite]
		}
		
		if !shouldFetch {
			continue
		}
		
		// Fetch the attribute with optimized functions
		switch fetchAttr {
		case "command_line":
			if cmdline, err := p.fetchCommandLineOptimized(pidInt); err == nil && cmdline != "" {
				attrs.PutStr(attrProcessCommandLine, cmdline)
				enriched = true
			}
		case "owner":
			if owner, err := p.fetchOwnerOptimized(pidInt); err == nil && owner != "" {
				attrs.PutStr(attrProcessOwner, owner)
				enriched = true
			}
		case "executable_path":
			if execPath, err := p.fetchExecutablePathOptimized(pidInt); err == nil && execPath != "" {
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
			readBytes, writeBytes, err := p.fetchIOOptimized(pidInt)
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

// containsString checks if a string is present in a slice
func containsString(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}