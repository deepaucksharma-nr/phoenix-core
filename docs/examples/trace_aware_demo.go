// Example program that demonstrates trace-aware reservoir sampling
//
// This program generates several traces with different characteristics
// and sends them to two PTE instances - one with trace-aware sampling
// enabled and one with standard sampling.
//
// To run this demo:
// 1. Start two PTE instances with the configuration from trace-aware-reservoir-sampling.yaml
// 2. Run this program: go run trace_aware_demo.go
// 3. Compare the exported spans between the two instances

package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	// Endpoints for the two PTE instances
	standardEndpoint  = "localhost:4317" // Standard sampling
	traceAwareEndpoint = "localhost:5317" // Trace-aware sampling
	
	serviceName = "trace-aware-demo"
	
	// Number of traces to generate
	traceCount = 100
	
	// Max spans per trace
	maxSpansPerTrace = 5
)

func main() {
	// Set up exporters and tracers
	standardTracer, standardCleanup, err := setupTracer(standardEndpoint, "standard")
	if err != nil {
		log.Fatalf("Failed to set up standard tracer: %v", err)
	}
	defer standardCleanup()
	
	traceAwareTracer, traceAwareCleanup, err := setupTracer(traceAwareEndpoint, "trace-aware")
	if err != nil {
		log.Fatalf("Failed to set up trace-aware tracer: %v", err)
	}
	defer traceAwareCleanup()
	
	fmt.Println("Generating traces...")
	
	// Generate traces with varying characteristics
	for i := 0; i < traceCount; i++ {
		// Randomly determine trace characteristics
		spanCount := rand.Intn(maxSpansPerTrace) + 1
		hasRoot := rand.Float32() > 0.2 // 80% of traces have a root span
		
		// Generate the same trace for both exporters
		traceID := generateTraceID()
		
		// Generate for standard sampler
		generateTrace(standardTracer, traceID, i, spanCount, hasRoot)
		
		// Generate for trace-aware sampler
		generateTrace(traceAwareTracer, traceID, i, spanCount, hasRoot)
		
		// Add a small delay between traces
		time.Sleep(10 * time.Millisecond)
		
		if i%10 == 0 {
			fmt.Printf("Generated %d traces...\n", i)
		}
	}
	
	fmt.Println("All traces generated. Check the PTE logs to see the differences in sampling behavior.")
	
	// Allow time for all spans to be exported
	time.Sleep(1 * time.Second)
}

// setupTracer configures an OpenTelemetry tracer that exports to the specified endpoint
func setupTracer(endpoint, instance string) (trace.Tracer, func(), error) {
	ctx := context.Background()
	
	// Set up the exporter
	conn, err := grpc.DialContext(ctx, endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to collector: %w", err)
	}
	
	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("failed to create trace exporter: %w", err)
	}
	
	// Set up the trace provider
	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceNameKey.String(fmt.Sprintf("%s-%s", serviceName, instance)),
		attribute.String("instance", instance),
	)
	
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	
	// Set the global trace provider and propagator
	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	
	// Return the tracer and a cleanup function
	cleanup := func() {
		_ = tracerProvider.Shutdown(ctx)
		_ = conn.Close()
	}
	
	return tracerProvider.Tracer(fmt.Sprintf("%s-%s", serviceName, instance)), cleanup, nil
}

// generateTraceID generates a random trace ID
func generateTraceID() string {
	traceID := make([]byte, 16)
	rand.Read(traceID)
	return fmt.Sprintf("%x", traceID)
}

// generateTrace creates a trace with the specified characteristics
func generateTrace(tracer trace.Tracer, traceIDStr string, index, spanCount int, hasRoot bool) {
	ctx := context.Background()
	
	// If the trace should have a root span
	if hasRoot {
		// Create the root span
		rootCtx, rootSpan := tracer.Start(
			ctx,
			fmt.Sprintf("root-span-%d", index),
			trace.WithNewRoot(),
			trace.WithSpanKind(trace.SpanKindServer),
		)
		defer rootSpan.End()
		
		// Add some attributes
		rootSpan.SetAttributes(
			attribute.Int("index", index),
			attribute.Bool("is_root", true),
			attribute.Int("total_spans", spanCount),
		)
		
		// Create child spans
		for j := 1; j < spanCount; j++ {
			childCtx, childSpan := tracer.Start(
				rootCtx,
				fmt.Sprintf("child-span-%d-%d", index, j),
				trace.WithSpanKind(trace.SpanKindClient),
			)
			
			// Add some attributes
			childSpan.SetAttributes(
				attribute.Int("index", index),
				attribute.Int("child_index", j),
				attribute.Bool("is_root", false),
			)
			
			// Maybe add grandchild spans
			if rand.Float32() > 0.7 && j < spanCount-1 {
				_, grandchildSpan := tracer.Start(
					childCtx,
					fmt.Sprintf("grandchild-span-%d-%d", index, j),
					trace.WithSpanKind(trace.SpanKindInternal),
				)
				
				// Add some attributes
				grandchildSpan.SetAttributes(
					attribute.Int("index", index),
					attribute.Int("grandchild_of", j),
					attribute.Bool("is_root", false),
				)
				
				grandchildSpan.End()
			}
			
			childSpan.End()
		}
	} else {
		// Create spans without a root span (orphaned spans)
		// In a real system, this might happen if spans arrive out of order
		// or if the root span is processed by a different collector
		
		// Create a "parent" span that itself has a parent that doesn't exist
		ctx, parentSpan := tracer.Start(
			ctx,
			fmt.Sprintf("orphan-parent-%d", index),
			trace.WithSpanKind(trace.SpanKindServer),
		)
		defer parentSpan.End()
		
		// Add some attributes
		parentSpan.SetAttributes(
			attribute.Int("index", index),
			attribute.Bool("is_orphaned", true), 
			attribute.Int("total_spans", spanCount),
		)
		
		// Create other orphaned spans
		for j := 1; j < spanCount; j++ {
			_, orphanSpan := tracer.Start(
				ctx,
				fmt.Sprintf("orphan-child-%d-%d", index, j),
				trace.WithSpanKind(trace.SpanKindClient),
			)
			
			// Add some attributes
			orphanSpan.SetAttributes(
				attribute.Int("index", index),
				attribute.Int("orphan_index", j),
				attribute.Bool("is_orphaned", true),
			)
			
			orphanSpan.End()
		}
	}
}