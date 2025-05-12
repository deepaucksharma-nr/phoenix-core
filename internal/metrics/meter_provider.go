package metrics

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/otel/metric"
)

// MockMeterProvider is a simple implementation of component.MeterProvider for testing
type MockMeterProvider struct{}

// Meter creates a new Meter with the given name
func (p *MockMeterProvider) Meter(name string, opts ...metric.MeterOption) metric.Meter {
	return nil // Return a nil meter as we don't need actual functionality for tests
}

// Start implementations of component.Component interface
func (p *MockMeterProvider) Start(context.Context, component.Host) error {
	return nil
}

// Shutdown implementations of component.Component interface
func (p *MockMeterProvider) Shutdown(context.Context) error {
	return nil
}