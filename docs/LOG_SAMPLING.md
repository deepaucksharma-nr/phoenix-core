# Log Sampling in Phoenix Telemetry Edge

## Overview

Phoenix Telemetry Edge (PTE) provides support for adaptive probabilistic sampling of logs through its custom adaptive head sampler component. This feature allows for significant volume reduction while preserving a statistically representative sample of logs.

## Key Features

- **Probabilistic Sampling**: Uses xxHash algorithm for efficient, deterministic sampling decisions
- **Field-Based Record ID Creation**: Creates consistent record IDs from configurable log record fields
- **Dynamic Sampling Rate**: Sampling probability can be dynamically adjusted by the PID controller
- **Attribute-Based Sampling**: Sample logs based on specific attributes like container name, log source, etc.

## How It Works

### Record ID Creation

The log sampler works by creating a unique "record ID" for each log entry based on configurable fields:

1. For each log record, the processor extracts values from specified fields (e.g., `log.file.path`, `k8s.pod.name`, `message`)
2. If any configured field is found in resource attributes or log attributes, its value is included
3. The values are sorted and concatenated with a separator to create a consistent record ID
4. If no configured fields are found, the timestamp and a random value are used instead

### Sampling Decision

Once a record ID is created:

1. The record ID is hashed using xxHash to produce a 64-bit hash value
2. The hash is converted to a value between 0 and 1
3. If this value is less than the current sampling probability, the log record is kept
4. Otherwise, the log record is dropped

### Dynamic Probability Adjustment

When used with the PID controller:

1. The controller monitors exporter queue utilization
2. If queue utilization exceeds thresholds, the controller adjusts the sampling probability
3. During high pressure periods, sampling becomes more aggressive (fewer logs kept)
4. During low pressure periods, sampling becomes less aggressive (more logs kept)

## Configuration

Enable and configure log sampling in your `values.yaml`:

```yaml
sampling:
  head:
    # Logs sampling configuration
    logs:
      enabled: true                                      # Enable log sampling
      initialProbability: 0.5                            # Initial sampling probability
      minProbability: 0.01                               # Minimum sampling probability
      maxProbability: 0.9                                # Maximum sampling probability
      recordIDFields: ["log.file.path", "k8s.pod.name", "message"]  # Fields to create record ID
```

## PID Controller Configuration

The PID controller can be configured to adjust sampling rates based on telemetry pressure:

```yaml
sampling:
  adaptive_controller:
    enabled: true
    intervalSeconds: 15
    targetQueueUtilizationHigh: 0.8   # If queue exceeds this, decrease sampling probability
    targetQueueUtilizationLow: 0.2    # If queue below this, increase sampling probability
    adjustmentFactorUp: 1.25          # Multiply by this factor to increase probability
    adjustmentFactorDown: 0.8         # Multiply by this factor to decrease probability
    ewmaAlpha: 0.3                    # Smoothing factor for queue utilization
    aggressiveDropFactor: 0.5         # Aggressive drop factor for sustained high pressure
    aggressiveDropWindowCount: 3      # Number of high utilization intervals before aggressive drop
```

## Metrics

The log sampler exposes the following metrics with the processor ID "adaptive_head_sampler_logs":

- `pte_sampling_head_probability`: Current sampling probability
- `pte_sampling_head_throughput`: Number of logs processed per second
- `pte_sampling_head_rejected_total`: Total number of logs rejected by the sampler

## Processing Order

The adaptive head sampler for logs runs in the following order in the logs pipeline:

```
memory_limiter → k8sattributes → resource → [transform/pii] → adaptive_head_sampler → batch → [routing]
```

It is important to maintain this order because:

1. Resource and attribute processors need to run first to provide the fields used for record ID creation
2. The PII transformer (if enabled) should anonymize fields before they're used for sampling
3. Batch processor should come after sampling to optimize batching of only the sampled logs

## Best Practices

1. **Start Conservative**: Begin with a higher sampling probability (e.g., 0.5) and adjust based on observed volume and significance
2. **Use Appropriate Fields**: Choose recordIDFields that ensure similar logs are sampled consistently
3. **Monitor Metrics**: Keep an eye on the rejection rates and throughput metrics
4. **Adjust Thresholds**: Fine-tune PID controller thresholds based on your specific workload characteristics
5. **Maintain Processor Order**: Ensure correct processor ordering in your pipeline configuration

## Limitations

- Cannot filter logs based on content in the current implementation
- No support for reservoir sampling for logs (only head sampling is available)
- Record ID fields must be present in resource attributes, log attributes, or message body