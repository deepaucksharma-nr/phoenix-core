package pidcontroller

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
)

const (
	// The value of "type" key in configuration.
	typeStr = "pid_controller"
)

// NewFactory returns a new factory for the PID controller extension.
func NewFactory() extension.Factory {
	return extension.NewFactory(
		typeStr,
		createDefaultConfig,
		createExtension,
		component.StabilityLevelBeta,
	)
}

// createExtension creates the extension based on this config.
func createExtension(
	ctx context.Context,
	set extension.CreateSettings,
	cfg component.Config,
) (extension.Extension, error) {
	return newPIDController(set, cfg)
}