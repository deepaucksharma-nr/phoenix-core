# Processor Order Testing

This document describes the processor order testing strategy implemented for the Phoenix Telemetry Edge (PTE) project.

## Overview

PTE uses a pipeline of processors to sample, transform, and prepare telemetry data (traces, logs, and metrics) for export. The order of these processors is critical for correct operation. For example:

- The adaptive head sampler must come before the reservoir sampler in the trace pipeline
- Resource and attribute processors must come before sampling processors
- Batch processors must come at the end of the pipeline

Incorrect processor ordering can lead to data loss, inefficient sampling, or broken functionality.

## Testing Approach

We use two complementary approaches to verify processor order:

### 1. Configuration Order Tests

Located in `tests/integration/order_test.go`, these tests verify that:

- Our Helm charts and default configurations define processors in the correct order
- Required processors are present in each pipeline
- Optional processors (when present) are in the correct position relative to other processors

These tests parse the configuration YAML and assert the correct ordering of processor names without actually running the collector.

### 2. Runtime Behavior Tests

Located in `integration/samplers/processor_order_test.go`, these tests:

- Create actual processor instances and chain them together
- Run telemetry data through the pipeline
- Verify that the sampling behavior is correct when the processors work together
- Check sampling ratios and ensure processors correctly modify the data

These tests focus on the interaction between the sampling processors:

- Adaptive Head Sampler + Reservoir Sampler for traces
- Adaptive Head Sampler for logs

## Important Processor Orders

### Traces Pipeline

Required order:
```
memory_limiter → k8sattributes → resource → [transform/pii] → adaptive_head_sampler → reservoir_sampler → batch → [routing]
```

### Logs Pipeline

Required order:
```
memory_limiter → k8sattributes → resource → [transform/pii] → adaptive_head_sampler → batch → [routing]
```

### Process Metrics Pipeline

Required order:
```
memory_limiter → k8sattributes → resource → fallback_proc_parser → transform/process_normalize → topn_process_metrics_filter → transform/drop_pid_attr → batch → [routing]
```

## Test Coverage

Our processor order tests ensure:

1. **Configuration Compliance**: Helm charts and configuration templates define processors in the correct order
2. **Runtime Behavior**: Processors work correctly when chained together in the correct order
3. **Data Flow**: Data flows through the pipeline as expected, with each processor correctly modifying the data

## Adding New Processors

When adding a new processor to any pipeline:

1. Update the order test in `tests/integration/order_test.go` to include the new processor
2. If the processor interacts with sampling, update the runtime tests in `integration/samplers/processor_order_test.go`
3. Add assertions to verify the processor's position relative to others
4. Update this documentation to include the new processor in the order diagram