package topnprocfilter

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/processor"
	"go.uber.org/zap"
)

func TestProcessorCreation(t *testing.T) {
	// Create a basic config
	cfg := &Config{
		TopN:            10,
		CPUThreshold:    0.05,
		MemoryThreshold: 0.05,
		IdleTTL:         "1m",
		RegistryID:      "test_proc_top_n",
	}
	
	set := processor.CreateSettings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
		BuildInfo: component.BuildInfo{},
	}
	
	proc, err := newProcessor(set, cfg)
	require.NoError(t, err)
	require.NotNil(t, proc)
	
	// Test Tunable interface
	assert.Equal(t, 1.0, proc.GetProbability())
	
	// Test setting a new value
	proc.SetProbability(0.5)
	assert.InDelta(t, 0.5, proc.GetProbability(), 0.01)
}

func TestQuickSelect(t *testing.T) {
	// Create test processes
	processes := []*processMetric{
		{PID: "1", ProcessName: "proc1", CPUUsage: 0.5},
		{PID: "2", ProcessName: "proc2", CPUUsage: 0.3},
		{PID: "3", ProcessName: "proc3", CPUUsage: 0.8},
		{PID: "4", ProcessName: "proc4", CPUUsage: 0.1},
		{PID: "5", ProcessName: "proc5", CPUUsage: 0.9},
	}
	
	// Get top 3 by CPU
	topN := getTopNByMetric(processes, 3, func(p *processMetric) float64 { return p.CPUUsage })
	
	// Check length
	assert.Equal(t, 3, len(topN))
	
	// Check top 3 PIDs (should be 5, 3, 1 in some order)
	pids := map[string]bool{}
	for _, proc := range topN {
		pids[proc.PID] = true
	}
	
	assert.True(t, pids["5"])
	assert.True(t, pids["3"])
	assert.True(t, pids["1"])
}

func TestProcessMetrics(t *testing.T) {
	// Create a basic config
	cfg := &Config{
		TopN:            2,
		CPUThreshold:    0.01,
		MemoryThreshold: 0.01,
		IdleTTL:         "1m",
		RegistryID:      "test_proc_top_n",
	}
	
	set := processor.CreateSettings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
		BuildInfo: component.BuildInfo{},
	}
	
	proc, err := newProcessor(set, cfg)
	require.NoError(t, err)
	
	// Create test metrics
	metrics := createTestMetrics()
	
	// Process the metrics
	filtered, err := proc.processMetrics(context.Background(), metrics)
	require.NoError(t, err)
	
	// The filtered metrics should contain only the top 2 processes
	// In this case, that should be proc3 and proc1
	resourceMetrics := filtered.ResourceMetrics()
	
	// Count how many process metrics we have in the filtered result
	processCount := 0
	for i := 0; i < resourceMetrics.Len(); i++ {
		rm := resourceMetrics.At(i)
		pid := rm.Resource().Attributes().GetStr("process.pid")
		if pid != "" {
			processCount++
			// Should be either proc1 or proc3
			assert.True(t, pid == "1" || pid == "3", "Expected process 1 or 3, got %s", pid)
		}
	}
	
	// We should have 2 process metrics
	assert.Equal(t, 2, processCount)
}

// createTestMetrics creates test metrics with process information
func createTestMetrics() pmetric.Metrics {
	metrics := pmetric.NewMetrics()
	
	// Process 1 - Medium CPU, Low Memory
	addProcessMetrics(metrics, "1", "proc1", 0.5, 0.1)
	
	// Process 2 - Low CPU, Low Memory
	addProcessMetrics(metrics, "2", "proc2", 0.1, 0.1)
	
	// Process 3 - High CPU, Medium Memory
	addProcessMetrics(metrics, "3", "proc3", 0.8, 0.4)
	
	// Non-process metrics (should be passed through)
	addNonProcessMetrics(metrics)
	
	return metrics
}

// addProcessMetrics adds process metrics to the metrics collection
func addProcessMetrics(metrics pmetric.Metrics, pid, name string, cpu, memory float64) {
	rm := metrics.ResourceMetrics().AppendEmpty()
	
	// Add process attributes
	rm.Resource().Attributes().PutStr("process.pid", pid)
	rm.Resource().Attributes().PutStr("process.executable.name", name)
	
	// Add CPU metric
	sm := rm.ScopeMetrics().AppendEmpty()
	sm.Scope().SetName("system.process")
	
	metricCPU := sm.Metrics().AppendEmpty()
	metricCPU.SetName("process.cpu.utilization")
	metricCPU.SetDescription("CPU utilization")
	metricCPU.SetUnit("1")
	
	dp := metricCPU.SetEmptyGauge().DataPoints().AppendEmpty()
	dp.SetDoubleValue(cpu)
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	
	// Add Memory metric
	metricMem := sm.Metrics().AppendEmpty()
	metricMem.SetName("process.memory.utilization")
	metricMem.SetDescription("Memory utilization")
	metricMem.SetUnit("1")
	
	dp = metricMem.SetEmptyGauge().DataPoints().AppendEmpty()
	dp.SetDoubleValue(memory)
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
}

// addNonProcessMetrics adds non-process metrics to the metrics collection
func addNonProcessMetrics(metrics pmetric.Metrics) {
	rm := metrics.ResourceMetrics().AppendEmpty()
	
	// Add some generic host attributes
	rm.Resource().Attributes().PutStr("host.name", "test-host")
	
	// Add a CPU metric
	sm := rm.ScopeMetrics().AppendEmpty()
	sm.Scope().SetName("system")
	
	metricCPU := sm.Metrics().AppendEmpty()
	metricCPU.SetName("system.cpu.utilization")
	metricCPU.SetDescription("CPU utilization")
	metricCPU.SetUnit("1")
	
	dp := metricCPU.SetEmptyGauge().DataPoints().AppendEmpty()
	dp.SetDoubleValue(0.3)
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
}