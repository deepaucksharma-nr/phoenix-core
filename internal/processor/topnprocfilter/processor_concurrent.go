package topnprocfilter

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/pdata/pmetric"
)

// processMetricsConcurrent implements the core top-N filtering logic
// with concurrent processing for better performance
func (tp *topNProcessor) processMetricsConcurrent(ctx context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
	// Create a new metrics collection for the filtered result
	result := pmetric.NewMetrics()
	
	// Process metrics and extract process information concurrently
	resourceMetrics := md.ResourceMetrics()
	resourceCount := resourceMetrics.Len()
	
	// Skip concurrency for small metric batches
	if resourceCount <= 4 {
		return tp.processMetricsSequential(ctx, md)
	}
	
	// Use concurrent processing for larger batches
	type processUpdate struct {
		pid          string
		processName  string
		cpuUsage     float64
		memoryUsage  float64
		updateNeeded bool
	}
	
	// Channel to collect process updates
	updateChan := make(chan processUpdate, resourceCount)
	
	// Use worker pool pattern with semaphore to limit concurrency
	maxWorkers := 8  // Reasonable default, could be made configurable
	if maxWorkers > resourceCount {
		maxWorkers = resourceCount
	}
	
	// Semaphore pattern for limiting concurrency
	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup
	
	// Process resources concurrently to extract metrics
	for i := 0; i < resourceMetrics.Len(); i++ {
		wg.Add(1)
		
		go func(index int) {
			// Acquire semaphore slot
			sem <- struct{}{}
			defer func() {
				// Release semaphore slot
				<-sem
				wg.Done()
			}()
			
			rm := resourceMetrics.At(index)
			resource := rm.Resource()
			
			// Extract process info from resource attributes
			pidVal, ok := resource.Attributes().Get("process.pid")
			pid := ""
			if ok {
				pid = pidVal.Str()
			}

			processNameVal, ok := resource.Attributes().Get("process.executable.name")
			processName := ""
			if ok {
				processName = processNameVal.Str()
			}
			
			// If not a process metric, just note that no update is needed
			if pid == "" || processName == "" {
				updateChan <- processUpdate{
					pid:          pid,
					processName:  processName,
					updateNeeded: false,
				}
				return
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
			
			// Send update info to the channel
			updateChan <- processUpdate{
				pid:          pid,
				processName:  processName,
				cpuUsage:     cpuUsage,
				memoryUsage:  memoryUsage,
				updateNeeded: updateProcess,
			}
		}(i)
	}
	
	// Close the channel when all workers are done
	go func() {
		wg.Wait()
		close(updateChan)
	}()
	
	// Collect and process all updates
	processesChanged := false
	
	for update := range updateChan {
		if update.updateNeeded {
			now := time.Now()
			nowUnix := now.Unix()

			proc := &processMetric{
				PID:                   update.pid,
				ProcessName:           update.processName,
				CPUUsage:              update.cpuUsage,
				MemoryUsage:           update.memoryUsage,
				LastUpdatedUnix:       nowUnix,
			}

			// Check if process meets threshold criteria
			if update.cpuUsage >= tp.config.CPUThreshold || update.memoryUsage >= tp.config.MemoryThreshold {
				proc.LastAboveThresholdUnix = nowUnix
			} else {
				// If process was previously tracked, preserve its last above threshold time
				if existingProc, ok := tp.processes.Load(update.pid); ok {
					proc.LastAboveThresholdUnix = existingProc.(*processMetric).LastAboveThresholdUnix
				}
			}
			
			tp.processes.Store(update.pid, proc)
			processesChanged = true
		}
	}
	
	// Invalidate cache if any processes were updated
	if processesChanged {
		tp.invalidateTopNCache()
	}
	
	// Apply top-N filtering - this will also update/use the cache
	_ = tp.getTopNProcesses() // Cache is updated inside this function
	
	// Filter the original metrics to only include top-N processes - do this concurrently too
	resourceMetrics = md.ResourceMetrics()
	filteredCount := atomic.Int64{}
	
	// Use a mutex to protect concurrent additions to the result
	var resultMu sync.Mutex
	
	// Process filtering concurrently
	wg = sync.WaitGroup{}
	
	for i := 0; i < resourceMetrics.Len(); i++ {
		wg.Add(1)
		
		go func(index int) {
			// Acquire semaphore slot
			sem <- struct{}{}
			defer func() {
				// Release semaphore slot
				<-sem
				wg.Done()
			}()
			
			rm := resourceMetrics.At(index)
			resource := rm.Resource()
	
			// Extract process info from resource attributes
			pidVal, ok := resource.Attributes().Get("process.pid")
			pid := ""
			if ok {
				pid = pidVal.Str()
			}
	
			// If not a process metric or is in top-N, include it
			if pid == "" || tp.isInTopN(pid) {
				resultMu.Lock()
				destRM := result.ResourceMetrics().AppendEmpty()
				rm.CopyTo(destRM)
				resultMu.Unlock()
			} else {
				// Count filtered processes
				filteredCount.Add(1)
			}
		}(i)
	}
	
	// Wait for all filtering to complete
	wg.Wait()
	
	// Track filtered processes for metrics
	filteredTotal := filteredCount.Load()
	if filteredTotal > 0 {
		tp.filteredCount.Add(filteredTotal)
	}
	
	return result, nil
}

// processMetricsSequential implements the sequential version of metric processing
// Used for small batches where concurrent overhead would be greater than benefit
func (tp *topNProcessor) processMetricsSequential(ctx context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
	// Create a new metrics collection for the filtered result
	result := pmetric.NewMetrics()
	
	// Process metrics and extract process information
	resourceMetrics := md.ResourceMetrics()
	processesChanged := false
	
	for i := 0; i < resourceMetrics.Len(); i++ {
		rm := resourceMetrics.At(i)
		resource := rm.Resource()
		
		// Extract process info from resource attributes
		pidVal, ok := resource.Attributes().Get("process.pid")
		pid := ""
		if ok {
			pid = pidVal.Str()
		}

		processNameVal, ok := resource.Attributes().Get("process.executable.name")
		processName := ""
		if ok {
			processName = processNameVal.Str()
		}
		
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
			nowUnix := now.Unix()

			proc := &processMetric{
				PID:                   pid,
				ProcessName:           processName,
				CPUUsage:              cpuUsage,
				MemoryUsage:           memoryUsage,
				LastUpdatedUnix:       nowUnix,
			}

			// Check if process meets threshold criteria
			if cpuUsage >= tp.config.CPUThreshold || memoryUsage >= tp.config.MemoryThreshold {
				proc.LastAboveThresholdUnix = nowUnix
			} else {
				// If process was previously tracked, preserve its last above threshold time
				if existingProc, ok := tp.processes.Load(pid); ok {
					proc.LastAboveThresholdUnix = existingProc.(*processMetric).LastAboveThresholdUnix
				}
			}
			
			tp.processes.Store(pid, proc)
			processesChanged = true
		}
	}
	
	// Invalidate cache if processes changed
	if processesChanged {
		tp.invalidateTopNCache()
	}
	
	// Apply top-N filtering - this will also update/use the cache
	_ = tp.getTopNProcesses() // Cache is updated inside this function
	
	// Filter the original metrics to only include top-N processes
	resourceMetrics = md.ResourceMetrics()
	filteredCount := 0

	for i := 0; i < resourceMetrics.Len(); i++ {
		rm := resourceMetrics.At(i)
		resource := rm.Resource()

		// Extract process info from resource attributes
		pidVal, ok := resource.Attributes().Get("process.pid")
		pid := ""
		if ok {
			pid = pidVal.Str()
		}

		// If not a process metric or is in top-N, include it
		if pid == "" || tp.isInTopN(pid) {
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