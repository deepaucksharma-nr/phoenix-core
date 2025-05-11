package topnprocfilter

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deepaucksharma-nr/phoenix-core/internal/metrics"
	"github.com/deepaucksharma-nr/phoenix-core/internal/pkg/tunableregistry"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/processor"
	"go.uber.org/zap"
)

// processMetric is a structure holding metric values for a specific process
type processMetric struct {
	PID              string
	ProcessName      string
	CPUUsage         float64
	MemoryUsage      float64
	LastUpdated      time.Time
	LastAboveThreshold time.Time
}

// topNProcessor implements top-N filtering for process metrics
type topNProcessor struct {
	logger    *zap.Logger
	config    *Config
	processes sync.Map  // stores processMetric by PID
	n         int       // current top-N value, can be adjusted via registry
	mu        sync.Mutex

	// Metrics
	metricsRegistry     *metrics.MetricsRegistry
	metricsTicker       *time.Ticker
	stopCh              chan struct{}
	processCount        atomic.Int64
	filteredCount       atomic.Int64
	currentCPUThreshold atomic.Float64
	currentMemThreshold atomic.Float64
}

// newProcessor creates a new topN process metrics filter processor
func newProcessor(set processor.CreateSettings, config *Config) (*topNProcessor, error) {
	idleTTL, err := time.ParseDuration(config.IdleTTL)
	if err != nil {
		return nil, err
	}

	proc := &topNProcessor{
		logger:          set.Logger,
		config:          config,
		n:               config.TopN,
		metricsRegistry: metrics.GetInstance(set.Logger),
		stopCh:          make(chan struct{}),
	}

	// Initialize thresholds for metrics
	proc.currentCPUThreshold.Store(config.CPUThreshold)
	proc.currentMemThreshold.Store(config.MemoryThreshold)

	// Initialize metrics
	if err := proc.initMetrics(); err != nil {
		return nil, fmt.Errorf("failed to initialize metrics: %w", err)
	}

	// Register with TunableRegistry
	registry := tunableregistry.GetInstance()
	registry.Register(proc)

	proc.logger.Info("Top-N process metrics filter processor created",
		zap.Int("top_n", config.TopN),
		zap.Float64("cpu_threshold", config.CPUThreshold),
		zap.Float64("memory_threshold", config.MemoryThreshold),
		zap.String("idle_ttl", config.IdleTTL))

	// Start cleanup goroutine
	go proc.periodicCleanup(idleTTL)

	return proc, nil
}

// ID implements the Tunable interface
func (tp *topNProcessor) ID() string {
	return tp.config.RegistryID
}

// SetValue implements the Tunable interface
func (tp *topNProcessor) SetValue(key string, value float64) {
	if key == "top_n" {
		tp.setTopN(int(value))
	} else if key == "cpu_threshold" {
		// Could add support for dynamically adjusting thresholds
		tp.logger.Debug("Adjusting CPU threshold not yet implemented", zap.Float64("value", value))
	} else if key == "memory_threshold" {
		// Could add support for dynamically adjusting thresholds
		tp.logger.Debug("Adjusting memory threshold not yet implemented", zap.Float64("value", value))
	}
	// Ignore other keys
}

// GetValue implements the Tunable interface
func (tp *topNProcessor) GetValue(key string) float64 {
	if key == "top_n" {
		tp.mu.Lock()
		defer tp.mu.Unlock()
		return float64(tp.n)
	} else if key == "top_n_ratio" {
		tp.mu.Lock()
		defer tp.mu.Unlock()
		return float64(tp.n) / float64(tp.config.TopN)
	} else if key == "cpu_threshold" {
		return tp.config.CPUThreshold
	} else if key == "memory_threshold" {
		return tp.config.MemoryThreshold
	}
	// Return 0 for unknown keys
	return 0
}

// setTopN sets the top-N value
func (tp *topNProcessor) setTopN(newN int) {
	if newN < 1 {
		newN = 1 // Ensure we keep at least 1 process
	}

	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.n = newN
	tp.logger.Info("Top-N value updated", zap.Int("new_n", newN))
}

// SetProbability implements the ProbSampler interface (backward compatibility)
func (tp *topNProcessor) SetProbability(p float64) {
	// Reinterpret probability as a percentage of the default N
	newN := int(p * float64(tp.config.TopN))
	tp.SetValue("top_n", float64(newN))
}

// GetProbability implements the ProbSampler interface (backward compatibility)
func (tp *topNProcessor) GetProbability() float64 {
	return tp.GetValue("top_n_ratio")
}

// Start implements the processor.Component interface
func (tp *topNProcessor) Start(_ context.Context, _ component.Host) error {
	return nil
}

// Shutdown implements the processor.Component interface
func (tp *topNProcessor) Shutdown(_ context.Context) error {
	return nil
}

// processMetrics implements the core top-N filtering logic
func (tp *topNProcessor) processMetrics(ctx context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
	// Create a new metrics collection for the filtered result
	result := pmetric.NewMetrics()
	
	// Process metrics and extract process information
	resourceMetrics := md.ResourceMetrics()
	for i := 0; i < resourceMetrics.Len(); i++ {
		rm := resourceMetrics.At(i)
		resource := rm.Resource()
		
		// Extract process info from resource attributes
		pid := resource.Attributes().GetStr("process.pid")
		processName := resource.Attributes().GetStr("process.executable.name")
		
		// Skip if not a process metric
		if pid == "" || processName == "" {
			// Pass through non-process metrics
			destRM := result.ResourceMetrics().AppendEmpty()
			rm.CopyTo(destRM)
			continue
		}
		
		// Process metrics to extract CPU and memory usage
		var cpuUsage, memoryUsage float64
		updateProcess := false
		
		scopeMetrics := rm.ScopeMetrics()
		for j := 0; j < scopeMetrics.Len(); j++ {
			sm := scopeMetrics.At(j)
			metrics := sm.Metrics()
			
			for k := 0; k < metrics.Len(); k++ {
				metric := metrics.At(k)
				
				// Extract CPU and memory metrics
				if metric.Name() == "process.cpu.utilization" {
					switch metric.Type() {
					case pmetric.MetricTypeGauge:
						cpuUsage = metric.Gauge().DataPoints().At(0).DoubleValue()
						updateProcess = true
					}
				} else if metric.Name() == "process.memory.utilization" {
					switch metric.Type() {
					case pmetric.MetricTypeGauge:
						memoryUsage = metric.Gauge().DataPoints().At(0).DoubleValue()
						updateProcess = true
					}
				}
			}
		}
		
		// Update process metrics in our collection if needed
		if updateProcess {
			now := time.Now()
			proc := &processMetric{
				PID:          pid,
				ProcessName:  processName,
				CPUUsage:     cpuUsage,
				MemoryUsage:  memoryUsage,
				LastUpdated:  now,
			}
			
			// Check if process meets threshold criteria
			if cpuUsage >= tp.config.CPUThreshold || memoryUsage >= tp.config.MemoryThreshold {
				proc.LastAboveThreshold = now
			} else {
				// If process was previously tracked, preserve its last above threshold time
				if existingProc, ok := tp.processes.Load(pid); ok {
					proc.LastAboveThreshold = existingProc.(*processMetric).LastAboveThreshold
				}
			}
			
			tp.processes.Store(pid, proc)
		}
	}
	
	// Apply top-N filtering
	topN := tp.getTopNProcesses()
	
	// Filter the original metrics to only include top-N processes
	resourceMetrics = md.ResourceMetrics()
	filteredCount := 0

	for i := 0; i < resourceMetrics.Len(); i++ {
		rm := resourceMetrics.At(i)
		resource := rm.Resource()

		// Extract process info from resource attributes
		pid := resource.Attributes().GetStr("process.pid")

		// If not a process metric or is in top-N, include it
		if pid == "" || isInTopN(pid, topN) {
			destRM := result.ResourceMetrics().AppendEmpty()
			rm.CopyTo(destRM)
		} else {
			// Count filtered processes
			filteredCount++
		}
	}

	// Track filtered processes for metrics
	if filteredCount > 0 {
		tp.filteredCount.Add(int64(filteredCount))
	}
	
	return result, nil
}

// getTopNProcesses returns the top N processes by CPU and memory usage
func (tp *topNProcessor) getTopNProcesses() []*processMetric {
	// Get current active processes
	var processes []*processMetric
	tp.processes.Range(func(key, value interface{}) bool {
		processes = append(processes, value.(*processMetric))
		return true
	})
	
	tp.mu.Lock()
	n := tp.n
	tp.mu.Unlock()
	
	if len(processes) <= n {
		return processes // Return all if we have fewer than N
	}
	
	// Return both CPU and memory top-N
	cpuTopN := getTopNByMetric(processes, n, func(p *processMetric) float64 { return p.CPUUsage })
	memTopN := getTopNByMetric(processes, n, func(p *processMetric) float64 { return p.MemoryUsage })
	
	// Merge the two sets, removing duplicates
	return mergeWithoutDuplicates(cpuTopN, memTopN)
}

// getTopNByMetric returns the top N processes by a specific metric using QuickSelect algorithm
func getTopNByMetric(processes []*processMetric, n int, valueFn func(*processMetric) float64) []*processMetric {
	if len(processes) <= n {
		return processes
	}
	
	// Copy to avoid modifying original
	processesCopy := make([]*processMetric, len(processes))
	copy(processesCopy, processes)
	
	// Use QuickSelect to find the Nth largest element
	quickSelect(processesCopy, 0, len(processesCopy)-1, n, valueFn)
	
	// Take the N largest elements (which are now at the beginning of the array)
	return processesCopy[:n]
}

// quickSelect implements the QuickSelect algorithm to find the kth largest element
func quickSelect(arr []*processMetric, left, right, k int, valueFn func(*processMetric) float64) {
	if left == right {
		return
	}
	
	pivotIndex := partition(arr, left, right, valueFn)
	
	if k == pivotIndex {
		return
	} else if k < pivotIndex {
		quickSelect(arr, left, pivotIndex-1, k, valueFn)
	} else {
		quickSelect(arr, pivotIndex+1, right, k, valueFn)
	}
}

// partition partitions the array around a pivot
func partition(arr []*processMetric, left, right int, valueFn func(*processMetric) float64) int {
	// Choose a pivot (rightmost element)
	pivotValue := valueFn(arr[right])
	
	// Index of smaller element
	i := left - 1
	
	for j := left; j < right; j++ {
		// If current element is larger than pivot
		if valueFn(arr[j]) > pivotValue {
			i++
			arr[i], arr[j] = arr[j], arr[i]
		}
	}
	
	// Place pivot in its correct position
	arr[i+1], arr[right] = arr[right], arr[i+1]
	return i + 1
}

// mergeWithoutDuplicates merges two slices of processes, removing duplicates
func mergeWithoutDuplicates(a, b []*processMetric) []*processMetric {
	seen := make(map[string]bool)
	result := []*processMetric{}
	
	// Add from both slices, skipping duplicates
	for _, proc := range append(a, b...) {
		if !seen[proc.PID] {
			seen[proc.PID] = true
			result = append(result, proc)
		}
	}
	
	return result
}

// isInTopN checks if a PID is in the top-N list
func isInTopN(pid string, topN []*processMetric) bool {
	for _, proc := range topN {
		if proc.PID == pid {
			return true
		}
	}
	return false
}

// periodicCleanup removes stale process entries that haven't been updated or above threshold
func (tp *topNProcessor) periodicCleanup(idleTTL time.Duration) {
	ticker := time.NewTicker(idleTTL / 2)
	defer ticker.Stop()
	
	for range ticker.C {
		now := time.Now()
		
		// Cleanup processes that haven't been above threshold for idleTTL
		var toRemove []string
		tp.processes.Range(func(key, value interface{}) bool {
			pid := key.(string)
			proc := value.(*processMetric)
			
			// Check last time process was above threshold
			if now.Sub(proc.LastAboveThreshold) > idleTTL {
				toRemove = append(toRemove, pid)
			}
			
			return true
		})
		
		// Remove stale processes
		for _, pid := range toRemove {
			tp.processes.Delete(pid)
		}
		
		if len(toRemove) > 0 {
			tp.logger.Debug("Cleaned up idle processes",
				zap.Int("removed_count", len(toRemove)))
		}
	}
}