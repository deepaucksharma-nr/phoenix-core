# Phoenix Telemetry Edge (PTE) Troubleshooting Guide

This document provides guidance for diagnosing and resolving common issues you might encounter when deploying and operating Phoenix Telemetry Edge.

## Table of Contents

- [Pod Won't Start](#pod-wont-start)
- [No Data Reaching New Relic](#no-data-reaching-new-relic)
- [High Memory Usage](#high-memory-usage)
- [High CPU Usage](#high-cpu-usage)
- [Disk Space Issues](#disk-space-issues)
- [Sampling Issues](#sampling-issues)
- [PID Controller Issues](#pid-controller-issues)
- [Common Error Messages](#common-error-messages)
- [Getting Logs and Diagnostic Information](#getting-logs-and-diagnostic-information)

## Pod Won't Start

### Symptom

The PTE pod is stuck in `Pending` or `ContainerCreating` status.

### Possible Causes and Solutions

1. **Insufficient resources**:
   ```bash
   kubectl describe pod <pod-name>
   ```
   Look for events indicating resource constraints.

   **Solution**: Adjust resource limits/requests in your Helm values:
   ```yaml
   collector:
     resources:
       limits:
         cpu: 1000m
         memory: 1Gi
       requests:
         cpu: 500m
         memory: 512Mi
   ```

2. **PVC creation issue**:
   ```bash
   kubectl get pvc
   kubectl describe pvc <pvc-name>
   ```

   **Solution**: Verify storage class exists, or disable persistence for testing:
   ```yaml
   persistence:
     enabled: false
   ```

3. **ConfigMap issues**:
   ```bash
   kubectl describe configmap <configmap-name>
   ```

   **Solution**: Check for YAML syntax errors in your values file.

## No Data Reaching New Relic

### Symptom

The pod is running but no data appears in New Relic.

### Possible Causes and Solutions

1. **License key issues**:
   Verify the license key secret is correct:
   ```bash
   kubectl get secret <license-key-secret> -o jsonpath='{.data.license-key}' | base64 --decode
   ```

   **Solution**: Create or update the license key secret.

2. **Network connectivity**:
   Check if the pod can reach New Relic endpoints:
   ```bash
   kubectl exec -it <pod-name> -- curl -I https://otlp.nr-data.net:4318
   ```

   **Solution**: Verify network policies, DNS, and outbound connectivity.

3. **Aggressive sampling**:
   Check if the head sampling probability has been reduced too much:
   ```bash
   kubectl exec -it <pod-name> -- curl http://localhost:8888/metrics | grep pte_sampling_head_probability
   ```

   **Solution**: Set a higher minimum probability:
   ```yaml
   sampling:
     head:
       minProbability: 0.1
   ```

4. **Check queue metrics**:
   ```bash
   kubectl exec -it <pod-name> -- curl http://localhost:8888/metrics | grep queue
   ```

   **Solution**: If queue size shows 0, data might not be reaching PTE. Check your instrumentation.

## High Memory Usage

### Symptom

The PTE pod is using a lot of memory or getting OOMKilled.

### Possible Causes and Solutions

1. **Insufficient memory limits**:
   ```bash
   kubectl top pod <pod-name>
   ```

   **Solution**: Increase memory limits:
   ```yaml
   collector:
     resources:
       limits:
         memory: 2Gi
   ```

2. **Large batches or spans**:
   Check memory_limiter metrics:
   ```bash
   kubectl exec -it <pod-name> -- curl http://localhost:8888/metrics | grep memory_limiter
   ```

   **Solution**: Adjust batch size and memory limiter settings:
   ```yaml
   # Custom processor.batch config
   customConfig:
     processors:
       batch:
         send_batch_size: 512
         timeout: 5s
   ```

3. **Reservoir size too large**:
   ```bash
   kubectl exec -it <pod-name> -- curl http://localhost:8888/metrics | grep reservoir
   ```

   **Solution**: Reduce reservoir size:
   ```yaml
   sampling:
     tail_reservoir:
       k_size: 2000
   ```

## High CPU Usage

### Symptom

The PTE pod is using a lot of CPU.

### Possible Causes and Solutions

1. **Check processor utilization**:
   ```bash
   kubectl exec -it <pod-name> -- curl http://localhost:8888/metrics | grep processor_cpu
   ```

   **Solution**: Identify which processor is consuming CPU and adjust its configuration.

2. **High ingress rate**:
   ```bash
   kubectl exec -it <pod-name> -- curl http://localhost:8888/metrics | grep receiver_accepted
   ```

   **Solution**: Increase CPU limits or use more aggressive head sampling.

3. **Verify PID controller activity**:
   ```bash
   kubectl logs <pod-name> | grep "PID controller"
   ```

   **Solution**: Adjust PID controller parameters for more aggressive sampling:
   ```yaml
   sampling:
     adaptive_controller:
       adjustmentFactorDown: 0.7
       aggressiveDropFactor: 0.4
   ```

## Disk Space Issues

### Symptom

PTE pod is running out of disk space.

### Possible Causes and Solutions

1. **PVC too small**:
   ```bash
   kubectl exec -it <pod-name> -- df -h
   ```

   **Solution**: Increase PVC size:
   ```yaml
   persistence:
     size: 10Gi
   ```

2. **Queue buffer growing**:
   ```bash
   kubectl exec -it <pod-name> -- ls -la /data/pte/queue
   ```

   **Solution**: Make sure PID controller is adjusting sampling rate correctly and check for New Relic connectivity issues.

3. **Checkpoint files growing**:
   ```bash
   kubectl exec -it <pod-name> -- ls -la /data/pte/reservoir_state.db
   ```

   **Solution**: May indicate an issue with the reservoir sampler.

## Sampling Issues

### Symptom

Too much or too little data is being sampled.

### Possible Causes and Solutions

1. **Head sampling rate too low/high**:
   ```bash
   kubectl exec -it <pod-name> -- curl http://localhost:8888/metrics | grep pte_sampling_head_probability
   ```

   **Solution**: Adjust initial, min and max probability:
   ```yaml
   sampling:
     head:
       initialProbability: 0.5
       minProbability: 0.1
       maxProbability: 1.0
   ```

2. **Reservoir size too small/large**:
   ```bash
   kubectl exec -it <pod-name> -- curl http://localhost:8888/metrics | grep pte_reservoir
   ```

   **Solution**: Adjust k_size and window duration:
   ```yaml
   sampling:
     tail_reservoir:
       k_size: 10000
       windowSeconds: 120
   ```

## PID Controller Issues

### Symptom

Sampling rates not adapting or oscillating wildly.

### Possible Causes and Solutions

1. **Controller not active**:
   ```bash
   kubectl logs <pod-name> | grep "PID controller"
   ```

   **Solution**: Ensure the controller is enabled:
   ```yaml
   sampling:
     adaptive_controller:
       enabled: true
   ```

2. **Adjustment factors too aggressive**:
   ```bash
   kubectl logs <pod-name> | grep "probability changed"
   ```

   **Solution**: Use gentler adjustment factors:
   ```yaml
   sampling:
     adaptive_controller:
       adjustmentFactorUp: 1.1
       adjustmentFactorDown: 0.9
       ewmaAlpha: 0.2
   ```

3. **Thresholds too close**:
   **Solution**: Widen the target range:
   ```yaml
   sampling:
     adaptive_controller:
       targetQueueUtilizationHigh: 0.8
       targetQueueUtilizationLow: 0.2
   ```

## Common Error Messages

### "Failed to load config: invalid configuration"

**Solution**: Check collector configuration for syntax errors:
```bash
kubectl describe configmap <configmap-name>
```

### "Failed to establish secure connection"

**Solution**: Check network connectivity to New Relic or other issues with TLS certificates.

### "License key invalid"

**Solution**: Verify your New Relic license key is correct.

### "Queue is full" or "buffer is full"

**Solution**: 
- Increase exporter queue buffer
- Adjust PID controller to be more aggressive 
- Check connectivity to New Relic

## Getting Logs and Diagnostic Information

### Collecting Basic Diagnostics

```bash
# Get pod status
kubectl get pods -l app.kubernetes.io/name=pte

# Get PTE pod logs
kubectl logs -l app.kubernetes.io/name=pte

# Get PTE metrics
kubectl port-forward svc/pte 8888:8888
curl http://localhost:8888/metrics | grep pte_
```

### Advanced Diagnostics

```bash
# Enable debug logging
kubectl edit deployment pte
# Add environment variable: DEBUG=true

# Dump exporter queue metrics
kubectl exec -it <pod-name> -- curl http://localhost:8888/metrics | grep exporter_queue

# View zpages 
kubectl port-forward svc/pte 55679:55679
# Access in browser: http://localhost:55679/debug/tracez

# Check PTE detailed status
kubectl port-forward svc/pte 13133:13133
curl http://localhost:13133/
```

### Support Information to Collect

When seeking help, collect the following information:

1. PTE version (`helm list`)
2. Complete logs (`kubectl logs <pod-name>`)
3. Configuration (values.yaml used and rendered collector config)
4. Metrics dump
5. Pod and PVC status
6. Description of the issue and when it started
7. Recent changes to the environment

## Additional Resources

If you're still experiencing issues after following this guide, refer to:

- [OpenTelemetry Collector Documentation](https://opentelemetry.io/docs/collector/)
- [New Relic OTLP Ingest Troubleshooting](https://docs.newrelic.com/docs/more-integrations/open-source-telemetry-integrations/opentelemetry/opentelemetry-setup/#troubleshooting)
- File an issue on our [GitHub repository](https://github.com/deepaucksharma-nr/phoenix-core/issues)