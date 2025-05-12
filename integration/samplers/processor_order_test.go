package samplers

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"github.com/deepaucksharma-nr/phoenix-core/internal/processor/adaptiveheadsampler"
	"github.com/deepaucksharma-nr/phoenix-core/internal/processor/reservoirsampler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processortest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// TestProcessorOrder verifies that the adaptive head sampler and reservoir sampler
// work correctly together in the expected order.
func TestProcessorOrder(t *testing.T) {
	// Create a test context
	ctx := context.Background()
	
	// Create test logger
	logger := zaptest.NewLogger(t)
	
	// Create a sink consumer to capture the final output
	sink := new(consumertest.TracesSink)
	
	// --- Set up the processors in order ---
	
	// 1. Create the adaptive head sampler with a fixed sampling rate for testing
	headCfg := &adaptiveheadsampler.Config{
		InitialProbability: 0.5, // Fixed 50% sampling rate for testing
		MinP:               0.01,
		MaxP:               0.99,
	}
	headFactory := adaptiveheadsampler.NewFactory()
	headCreateSettings := processortest.NewNopCreateSettings()
	headCreateSettings.Logger = logger
	
	// Create head sampler and validate it
	headSampler, err := headFactory.CreateTracesProcessor(
		ctx, 
		headCreateSettings, 
		headCfg, 
		nil, // We'll set this later
	)
	require.NoError(t, err)
	require.NotNil(t, headSampler)
	
	// 2. Create the reservoir sampler
	resCfg := &reservoirsampler.Config{
		SizeK:                   10, // Small size for testing
		WindowDuration:          "5s",
		CheckpointPath:          t.TempDir() + "/reservoir_test.db",
		CheckpointInterval:      "1s",
		DbCompactionScheduleCron: "", // Disable for testing
		DbCompactionTargetSize:   1024 * 1024, // 1MB
	}
	resFactory := reservoirsampler.NewFactory()
	resCreateSettings := processortest.NewNopCreateSettings()
	resCreateSettings.Logger = logger
	
	// Create reservoir sampler and validate it
	resSampler, err := resFactory.CreateTracesProcessor(
		ctx, 
		resCreateSettings, 
		resCfg, 
		sink, // Set the sink as the next consumer
	)
	require.NoError(t, err)
	require.NotNil(t, resSampler)
	
	// Connect the head sampler to the reservoir sampler
	err = headSampler.(*component.WrapperProcessor).SetNextConsumer(resSampler)
	require.NoError(t, err)
	
	// Start the processors
	err = headSampler.Start(ctx, componenttest.NewNopHost())
	require.NoError(t, err)
	defer headSampler.Shutdown(ctx)
	
	err = resSampler.Start(ctx, componenttest.NewNopHost())
	require.NoError(t, err)
	defer resSampler.Shutdown(ctx)
	
	// --- Generate test data ---
	
	// Create 100 traces to send through the pipeline
	traceCount := 100
	traces := generateTestTraces(traceCount)
	
	// Process the traces through the head sampler
	err = headSampler.ConsumeTraces(ctx, traces)
	require.NoError(t, err)
	
	// Wait for processing to complete
	time.Sleep(2 * time.Second)
	
	// Verify sampling behavior
	
	// 1. With 50% head sampling rate, expect approximately traceCount/2 traces to be forwarded
	//    to the reservoir, with some statistical variance
	expectedMinHeadSampled := int(float64(traceCount) * 0.35) // Lower bound with variance
	expectedMaxHeadSampled := int(float64(traceCount) * 0.65) // Upper bound with variance
	
	// 2. Reservoir should hold at most its maximum size
	maxReservoirSize := min(resCfg.SizeK, expectedMaxHeadSampled)
	
	// Verify the results
	t.Logf("Generated %d traces", traceCount)
	t.Logf("Received %d sampled traces in the sink", sink.SpanCount())
	
	// Verify that the number of sampled traces is within expected range
	assert.GreaterOrEqual(t, sink.SpanCount(), 1, "At least some traces should be sampled")
	assert.LessOrEqual(t, sink.SpanCount(), maxReservoirSize, 
		"Sampled trace count should not exceed reservoir size")
	
	// Log the sampling ratio for information
	samplingRatio := float64(sink.SpanCount()) / float64(traceCount)
	t.Logf("Overall sampling ratio: %.2f", samplingRatio)
}

func TestLogsProcessorOrder(t *testing.T) {
	// Create a test context
	ctx := context.Background()
	
	// Create test logger
	logger := zaptest.NewLogger(t)
	
	// Create a sink consumer to capture the final output
	sink := new(consumertest.LogsSink)
	
	// --- Set up the processors in order ---
	
	// 1. Create the adaptive head sampler with a fixed sampling rate for testing
	headCfg := &adaptiveheadsampler.Config{
		InitialProbability: 0.5, // Fixed 50% sampling rate for testing
		MinP:               0.01,
		MaxP:               0.99,
		RecordIDFields:     []string{"message", "log.level"},
	}
	headFactory := adaptiveheadsampler.NewFactory()
	headCreateSettings := processortest.NewNopCreateSettings()
	headCreateSettings.Logger = logger
	
	// Create head sampler and validate it
	headSampler, err := headFactory.CreateLogsProcessor(
		ctx, 
		headCreateSettings, 
		headCfg, 
		sink, // Set the sink as the next consumer
	)
	require.NoError(t, err)
	require.NotNil(t, headSampler)
	
	// Start the processor
	err = headSampler.Start(ctx, componenttest.NewNopHost())
	require.NoError(t, err)
	defer headSampler.Shutdown(ctx)
	
	// --- Generate test data ---
	
	// Create logs to send through the pipeline
	logCount := 100
	logs := generateTestLogs(logCount)
	
	// Process the logs through the head sampler
	err = headSampler.ConsumeLogs(ctx, logs)
	require.NoError(t, err)
	
	// Wait for processing to complete
	time.Sleep(1 * time.Second)
	
	// Verify sampling behavior
	
	// 1. With 50% head sampling rate, expect approximately logCount/2 logs to be sampled,
	//    with some statistical variance
	expectedMinHeadSampled := int(float64(logCount) * 0.35) // Lower bound with variance
	expectedMaxHeadSampled := int(float64(logCount) * 0.65) // Upper bound with variance
	
	// Verify the results
	actualSampled := sink.LogRecordCount()
	t.Logf("Generated %d logs", logCount)
	t.Logf("Received %d sampled logs in the sink", actualSampled)
	
	// Verify that the number of sampled logs is within expected range
	assert.GreaterOrEqual(t, actualSampled, expectedMinHeadSampled, 
		"At least 35%% of logs should be sampled")
	assert.LessOrEqual(t, actualSampled, expectedMaxHeadSampled, 
		"No more than 65%% of logs should be sampled")
	
	// Log the sampling ratio for information
	samplingRatio := float64(actualSampled) / float64(logCount)
	t.Logf("Logs sampling ratio: %.2f", samplingRatio)
}

// generateTestTraces creates test trace data with the specified number of traces.
func generateTestTraces(count int) ptrace.Traces {
	traces := ptrace.NewTraces()
	
	for i := 0; i < count; i++ {
		resourceSpans := traces.ResourceSpans().AppendEmpty()
		
		// Add resource attributes
		resource := resourceSpans.Resource()
		resource.Attributes().PutStr("service.name", fmt.Sprintf("test-service-%d", i%5))
		resource.Attributes().PutStr("service.version", "1.0.0")
		resource.Attributes().PutStr("deployment.environment", "test")
		
		scopeSpans := resourceSpans.ScopeSpans().AppendEmpty()
		scopeSpans.Scope().SetName("test-scope")
		scopeSpans.Scope().SetVersion("1.0.0")
		
		span := scopeSpans.Spans().AppendEmpty()
		span.SetName(fmt.Sprintf("test-span-%d", i))
		span.SetTraceID(generateTraceID(i))
		span.SetSpanID(generateSpanID(i))
		span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(-10 * time.Millisecond)))
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now()))
		
		// Add span attributes
		span.Attributes().PutStr("http.method", "GET")
		span.Attributes().PutStr("http.url", fmt.Sprintf("http://example.com/api/%d", i))
		span.Attributes().PutInt("http.status_code", 200)
	}
	
	return traces
}

// generateTestLogs creates test log data with the specified number of log records.
func generateTestLogs(count int) plog.Logs {
	logs := plog.NewLogs()

	for i := 0; i < count; i++ {
		resourceLogs := logs.ResourceLogs().AppendEmpty()

		// Add resource attributes
		resource := resourceLogs.Resource()
		resource.Attributes().PutStr("service.name", fmt.Sprintf("test-service-%d", i%5))
		resource.Attributes().PutStr("service.version", "1.0.0")
		resource.Attributes().PutStr("deployment.environment", "test")

		scopeLogs := resourceLogs.ScopeLogs().AppendEmpty()
		scopeLogs.Scope().SetName("test-scope")
		scopeLogs.Scope().SetVersion("1.0.0")

		logRecord := scopeLogs.LogRecords().AppendEmpty()
		logRecord.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
		logRecord.SetSeverityNumber(plog.SeverityNumber(i % 5)) // Vary severity

		severity := "INFO"
		switch i % 5 {
		case 1:
			severity = "WARN"
		case 2:
			severity = "ERROR"
		case 3:
			severity = "DEBUG"
		case 4:
			severity = "FATAL"
		}
		logRecord.SetSeverityText(severity)

		// Set body and attributes
		logRecord.Body().SetStr(fmt.Sprintf("This is test log message #%d", i))
		logRecord.Attributes().PutStr("log.level", severity)
		logRecord.Attributes().PutInt("log.id", int64(i))
		logRecord.Attributes().PutStr("log.source", "integration-test")
	}

	return logs
}

// generateTraceID creates a trace ID from an integer
func generateTraceID(id int) pcommon.TraceID {
	var traceID [16]byte
	binary.LittleEndian.PutUint64(traceID[:8], uint64(id))
	binary.LittleEndian.PutUint64(traceID[8:], uint64(id))
	return pcommon.TraceID(traceID)
}

// generateSpanID creates a span ID from an integer
func generateSpanID(id int) pcommon.SpanID {
	var spanID [8]byte
	binary.LittleEndian.PutUint64(spanID[:], uint64(id))
	return pcommon.SpanID(spanID)
}

// min returns the smaller of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}