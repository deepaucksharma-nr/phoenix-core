package adaptiveheadsampler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
	"go.uber.org/zap"
)

func TestProcessorNoSampling(t *testing.T) {
	// Create a processor with sampling probability set to 0
	// This should drop all traces
	cfg := &Config{
		InitialProbability: 0,
		MinP:               0,
		MaxP:               1,
		HashSeedConfig:     "XORTraceID",
	}
	
	sink := new(consumertest.TracesSink)
	set := processor.CreateSettings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
		BuildInfo: component.BuildInfo{},
	}
	
	proc, err := newProcessor(set, cfg, sink)
	require.NoError(t, err)
	
	// Create a simple trace with one span
	traces := createTraces(1)
	
	// Process the traces
	err = proc.ConsumeTraces(context.Background(), traces)
	require.NoError(t, err)
	
	// Verify no traces were forwarded to the next consumer
	assert.Equal(t, 0, len(sink.AllTraces()))
}

func TestProcessorFullSampling(t *testing.T) {
	// Create a processor with sampling probability set to 1
	// This should keep all traces
	cfg := &Config{
		InitialProbability: 1,
		MinP:               0,
		MaxP:               1,
		HashSeedConfig:     "XORTraceID",
	}
	
	sink := new(consumertest.TracesSink)
	set := processor.CreateSettings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
		BuildInfo: component.BuildInfo{},
	}
	
	proc, err := newProcessor(set, cfg, sink)
	require.NoError(t, err)
	
	// Create a simple trace with one span
	traces := createTraces(1)
	
	// Process the traces
	err = proc.ConsumeTraces(context.Background(), traces)
	require.NoError(t, err)
	
	// Verify all traces were forwarded to the next consumer
	assert.Equal(t, 1, len(sink.AllTraces()))
	assert.Equal(t, 1, sink.AllTraces()[0].SpanCount())
}

func TestProcessorPartialSampling(t *testing.T) {
	// Create a processor with sampling probability set to 0.5
	// This should keep approximately half of the traces
	cfg := &Config{
		InitialProbability: 0.5,
		MinP:               0,
		MaxP:               1,
		HashSeedConfig:     "XORTraceID",
	}
	
	sink := new(consumertest.TracesSink)
	set := processor.CreateSettings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
		BuildInfo: component.BuildInfo{},
	}
	
	proc, err := newProcessor(set, cfg, sink)
	require.NoError(t, err)
	
	// Create 1000 traces to have a statistically significant sample
	traces := createTraces(1000)
	
	// Process the traces
	err = proc.ConsumeTraces(context.Background(), traces)
	require.NoError(t, err)
	
	// Count the number of traces that were sampled
	sampledCount := 0
	if len(sink.AllTraces()) > 0 {
		sampledCount = sink.AllTraces()[0].SpanCount()
	}
	
	// With 1000 traces and p=0.5, we expect around 500 traces to be sampled
	// Allow a reasonable margin for statistical variation
	assert.InDelta(t, 500, sampledCount, 100, "Expected approximately 500 sampled traces")
}

func TestSetProbability(t *testing.T) {
	// Create a processor with initial probability 0.5
	cfg := &Config{
		InitialProbability: 0.5,
		MinP:               0.1,
		MaxP:               0.9,
		HashSeedConfig:     "XORTraceID",
	}
	
	sink := new(consumertest.TracesSink)
	set := processor.CreateSettings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
		BuildInfo: component.BuildInfo{},
	}
	
	proc, err := newProcessor(set, cfg, sink)
	require.NoError(t, err)
	
	// Verify initial probability
	p := proc.(*adaptiveHeadSamplerProcessor)
	assert.Equal(t, 0.5, p.GetProbability())
	
	// Test setting probability within bounds
	p.SetProbability(0.7)
	assert.Equal(t, 0.7, p.GetProbability())
	
	// Test setting probability below min
	p.SetProbability(0.05)
	assert.Equal(t, 0.1, p.GetProbability(), "Probability should be clamped to min_p")
	
	// Test setting probability above max
	p.SetProbability(1.0)
	assert.Equal(t, 0.9, p.GetProbability(), "Probability should be clamped to max_p")
}

// Helper function to create test traces
func createTraces(spanCount int) ptrace.Traces {
	traces := ptrace.NewTraces()
	
	for i := 0; i < spanCount; i++ {
		rs := traces.ResourceSpans().AppendEmpty()
		ss := rs.ScopeSpans().AppendEmpty()
		span := ss.Spans().AppendEmpty()
		
		// Generate a unique trace ID for each span
		// This ensures they're sampled independently
		traceID := [16]byte{}
		traceID[0] = byte(i)
		traceID[1] = byte(i >> 8)
		span.SetTraceID(pcommon.TraceID(traceID))
		
		span.SetSpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8})
		span.SetName("test-span")
	}
	
	return traces
}