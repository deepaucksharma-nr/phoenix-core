# Trace-Aware Buffer Standalone Tests

This directory contains standalone tests for the trace-aware buffer implementation
used in the reservoir sampler. These tests are designed to verify the core functionality
of the trace buffer system without dependencies on the full OpenTelemetry framework.

## Test Files

- `trace_buffer_test.go`: Unit tests for the trace buffer functionality
- `integration_test.go`: Integration tests for trace-aware sampling workflow
- `mock_metrics.go`: Mock metrics registry for testing

## Why Standalone Tests?

The trace-aware buffer is a critical component of the reservoir sampler, and it's
important to thoroughly test its functionality in isolation. These standalone tests
allow us to:

1. Test the trace buffer logic without dependencies on the full OTel framework
2. Verify edge cases and race conditions that are difficult to test in the full system
3. Ensure the core algorithms are correct before integration
4. Run tests quickly without the overhead of the full system

## Running the Tests

To run the standalone tests:

```bash
# Run all tests in the standalone_test directory
go test ./internal/processor/reservoirsampler/standalone_test/

# Run with race detection
go test -race ./internal/processor/reservoirsampler/standalone_test/

# Run verbose output
go test -v ./internal/processor/reservoirsampler/standalone_test/

# Run a specific test
go test ./internal/processor/reservoirsampler/standalone_test/ -run TestTraceAwareIntegration
```

## Test Coverage

These tests cover:

1. **Basic Trace Buffer Operations**:
   - Adding spans to trace buffers
   - Root span detection
   - Timeout handling

2. **Trace Buffer Map Operations**:
   - Adding spans to the right trace buffers
   - Tracking completed and timed-out traces
   - Cleanup of expired traces

3. **Integration Tests**:
   - End-to-end trace-aware sampling workflow
   - Concurrency and race conditions

## Relationship to Main Tests

These standalone tests complement the main processor tests in the parent directory.
The main tests verify the integration of the trace-aware buffer with the rest of
the reservoir sampler, while these standalone tests focus on the core functionality
and edge cases of the trace buffer itself.

## Mocks

The `mock_metrics.go` file provides a simplified mock implementation of the metrics
registry used in the main codebase. This allows the tests to verify that metrics
are correctly recorded without depending on the actual metrics implementation.