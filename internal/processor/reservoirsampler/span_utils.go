package reservoirsampler

import (
	"encoding/hex"
	"fmt"

	"github.com/deepaucksharma-nr/phoenix-core/internal/processor/reservoirsampler/spanprotos"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// spanSummaryFromSpan creates a SpanSummary protobuf message from a ptrace.Span
func spanSummaryFromSpan(
	span ptrace.Span,
	resource pcommon.Resource,
	scope pcommon.InstrumentationScope,
) *spanprotos.SpanSummary {
	summary := &spanprotos.SpanSummary{
		TraceId:           span.TraceID(),
		SpanId:            span.SpanID(),
		Name:              span.Name(),
		Kind:              span.Kind().String(),
		StartTimeUnixNano: int64(span.StartTimestamp()),
		EndTimeUnixNano:   int64(span.EndTimestamp()),
		StatusCode:        int32(span.Status().Code()),
		StatusMessage:     span.Status().Message(),
	}

	// Add parent span ID if it exists
	if !span.ParentSpanID().IsEmpty() {
		summary.ParentSpanId = span.ParentSpanID()
	}

	// Add service name from resource attributes if available
	if serviceName, ok := resource.Attributes().Get("service.name"); ok {
		summary.ServiceName = serviceName.Str()
	}

	// Add tenant ID from resource attributes if available for multi-tenant support
	if tenantID, ok := resource.Attributes().Get("tenant.id"); ok {
		summary.TenantId = tenantID.Str()
	}

	return summary
}

// createSpanKey generates a unique key for a span in the LRU cache
func createSpanKey(traceID pcommon.TraceID, spanID pcommon.SpanID) string {
	return fmt.Sprintf("%s-%s", hex.EncodeToString(traceID[:]), hex.EncodeToString(spanID[:]))
}

// spanSummaryToSpan converts a SpanSummary back to a ptrace.Span
// Note: This is lossy as we only keep a summary of important fields
func spanSummaryToTraces(summaries []*spanprotos.SpanSummary) ptrace.Traces {
	traces := ptrace.NewTraces()

	// Group by service name/tenant ID for more efficient organization
	resourceSpans := make(map[string]map[string][]spanprotos.SpanSummary)

	// First pass - group by service and tenant
	for _, summary := range summaries {
		serviceName := summary.ServiceName
		if serviceName == "" {
			serviceName = "unknown_service"
		}

		tenantID := summary.TenantId
		if tenantID == "" {
			tenantID = "default"
		}

		// Ensure maps are initialized
		if _, ok := resourceSpans[serviceName]; !ok {
			resourceSpans[serviceName] = make(map[string][]spanprotos.SpanSummary)
		}

		if _, ok := resourceSpans[serviceName][tenantID]; !ok {
			resourceSpans[serviceName][tenantID] = []spanprotos.SpanSummary{}
		}

		resourceSpans[serviceName][tenantID] = append(resourceSpans[serviceName][tenantID], *summary)
	}

	// Second pass - create the spans
	for serviceName, tenantMap := range resourceSpans {
		for tenantID, spanSummaries := range tenantMap {
			// Create resource for this service/tenant
			rs := traces.ResourceSpans().AppendEmpty()
			rs.Resource().Attributes().PutStr("service.name", serviceName)
			if tenantID != "default" {
				rs.Resource().Attributes().PutStr("tenant.id", tenantID)
			}

			// Create default scope
			ss := rs.ScopeSpans().AppendEmpty()
			ss.Scope().SetName("reservoir_sampler")

			// Add spans for this service/tenant
			for _, summary := range spanSummaries {
				span := ss.Spans().AppendEmpty()

				// Copy basic fields from summary
				span.SetName(summary.Name)
				span.SetTraceID(pcommon.TraceID(summary.TraceId))
				span.SetSpanID(pcommon.SpanID(summary.SpanId))

				if len(summary.ParentSpanId) > 0 {
					span.SetParentSpanID(pcommon.SpanID(summary.ParentSpanId))
				}

				span.SetStartTimestamp(pcommon.Timestamp(summary.StartTimeUnixNano))
				span.SetEndTimestamp(pcommon.Timestamp(summary.EndTimeUnixNano))

				// Set status if it exists
				span.Status().SetCode(ptrace.StatusCode(summary.StatusCode))
				span.Status().SetMessage(summary.StatusMessage)

				// We lose span attributes, events, and links in the summary
				// Set a marker attribute to indicate this is a summary
				span.Attributes().PutBool("pte.summary", true)
			}
		}
	}

	return traces
}
