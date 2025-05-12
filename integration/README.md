# PTE Integration Tests

This directory contains integration tests for the Phoenix Telemetry Edge (PTE) components. These tests verify that multiple components work correctly together in realistic scenarios.

## Test Categories

### Sampler Processors Order Testing (`samplers` directory)

Tests in this directory verify the proper operation of the sampling pipeline, including:

1. **Processor Order Tests**: Verify that processors work correctly when chained together in the expected order:
   - Adaptive Head Sampler → Reservoir Sampler
   - Logs Adaptive Head Sampler

These tests ensure that:
- Components process data correctly when chained together
- Sampling rates are applied as expected
- Components handle data mutation correctly

## Running the Tests

Run all integration tests with:

```bash
make test-integration
```

Run specific integration test directories with:

```bash
go test -v ./integration/samplers/...
```

## Adding New Tests

When adding new integration tests:

1. Create a new test file in the appropriate directory
2. Ensure tests verify multi-component interaction, not just single component functionality
3. Test realistic scenarios that mirror production environments
4. Keep test resource usage reasonable for CI environments
5. Add proper cleanup in test teardown