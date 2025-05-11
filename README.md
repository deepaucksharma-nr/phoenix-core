# Phoenix Telemetry Edge (PTE)

> Lean NRDoT-Only, Adaptive Dual‐Phase Sampling

Phoenix Telemetry Edge (PTE) is a specialized OpenTelemetry collector designed for efficient telemetry processing with integrated adaptive sampling to reduce data volume while preserving signal integrity.

## Features

- **Adaptive Dual-Phase Sampling**: Combines head-based probability sampling with reservoir sampling
- **Self-tuning**: Automatically adjusts sampling rates based on exporter queue backpressure
- **PII Protection**: Field-level redaction and hashing for sensitive data
- **High Durability**: PVC-backed persistence for both exporter queues and sampling state
- **Multi-tenant Support**: Route data to different destinations based on tenant ID
- **Kubernetes Native**: Designed to run as a Kubernetes deployment or DaemonSet

## Architecture

PTE implements a custom NRDoT (New Relic OpenTelemetry Collector) distribution with embedded adaptive sampling logic. It employs a two-stage sampling strategy:

1. **Adaptive Probabilistic Head Sampler**: Provides dynamic, real-time adjustment of sampling probability based on exporter queue backpressure.
2. **Reservoir Tail Sampler**: Implements Algorithm R to retain a statistically representative sample from data that passes head sampling.

The sampling rates are controlled by a lightweight PID-style controller that monitors queue pressure and adjusts head sampling probability accordingly.

![Architecture Diagram](docs/architecture-diagram.png)

## Quick Start

### Prerequisites

- Kubernetes cluster (v1.16+)
- Helm v3
- Access to New Relic OTLP ingest endpoint
- New Relic License Key

### Installation

1. **Create a secret for your New Relic License Key**:

   ```bash
   kubectl create secret generic nr-license-key \
     --from-literal=license-key=YOUR_LICENSE_KEY
   ```

2. **Create a secret for PII salts**:

   ```bash
   kubectl create secret generic pte-pii-salts \
     --from-literal=DEFAULT_PII_SALT=YOUR_SALT_STRING
   ```

3. **Install PTE using Helm**:

   ```bash
   helm install pte helm/pte \
     --set persistence.enabled=true \
     --set persistence.size=5Gi
   ```

   For a minimal configuration, you can use the demo values:

   ```bash
   helm install pte helm/pte -f helm/pte/ci/values.demo.yaml
   ```

4. **Verify the installation**:

   ```bash
   kubectl get pods -l app.kubernetes.io/name=pte
   kubectl port-forward svc/pte 8888:8888
   curl http://localhost:8888/metrics
   ```

### Docker Image

You can pull the pre-built Docker image:

```bash
docker pull ghcr.io/deepaucksharma-nr/pte-collector:v0.1.0
```

## Configuration

PTE is configured via Helm values which generate the OTel Collector configuration.

### Basic Configuration

```yaml
# values.yaml
sampling:
  head:
    initialProbability: 0.25  # Initial sampling probability (0.0-1.0)
    minProbability: 0.01      # Minimum probability allowed
    maxProbability: 0.9       # Maximum probability allowed
  tail_reservoir:
    k_size: 5000              # Maximum number of spans in the reservoir per window
    windowSeconds: 60         # Window size in seconds
  adaptive_controller:
    enabled: true             # Enable the PID controller
    intervalSeconds: 15       # How often the controller runs
    targetQueueUtilizationHigh: 0.8  # High queue threshold (trigger decrease)
    targetQueueUtilizationLow: 0.2   # Low queue threshold (trigger increase)
    adjustmentFactorUp: 1.25         # Multiply p by this when queue is low
    adjustmentFactorDown: 0.8        # Multiply p by this when queue is high
```

### PII Protection

PTE supports PII protection through OTTL-based transformations:

```yaml
pii:
  fieldsToHash: ["user.email", "http.request.header.authorization"]
  defaultSaltSecret:
    name: "pte-pii-salts"
    key: "DEFAULT_PII_SALT"
```

### Multi-tenancy

Enable multi-tenant routing:

```yaml
multiTenant:
  enabled: true
  tenants:
    - id: "tenant1"
      name: "Tenant 1"
      licenseKeySecretName: "tenant1-license-key"
    - id: "tenant2"
      name: "Tenant 2"
      licenseKeySecretName: "tenant2-license-key"
```

### Persistence

Configure persistence for queue durability and checkpoint state:

```yaml
persistence:
  enabled: true
  size: 5Gi
  storageClass: "standard"  # Use your storage class
  accessMode: ReadWriteOnce
```

## Monitoring

PTE exposes several metrics to monitor its operation:

- `pte_sampling_head_probability_ratio`: Current sampling probability ratio
- `pte_reservoir_checkpoint_age_seconds`: Time since last reservoir checkpoint
- `otelcol_exporter_queue_size`: Current exporter queue size
- `otelcol_exporter_queue_capacity`: Maximum exporter queue capacity

## Build from Source

```bash
# Clone the repository
git clone https://github.com/deepaucksharma-nr/phoenix-core

# Install dependencies
make deps

# Build the binary
make build

# Run tests
make test

# Build the Docker image
make docker-build
```

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for details.

## License

This project is licensed under the Apache 2.0 License - see the [LICENSE](LICENSE) file for details.

## Troubleshooting

For troubleshooting guidance, please refer to [TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md).