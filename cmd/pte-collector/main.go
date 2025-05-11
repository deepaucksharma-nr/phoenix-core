package main

import (
	"github.com/deepaucksharma-nr/phoenix-core/internal/extension/pidcontroller"
	"github.com/deepaucksharma-nr/phoenix-core/internal/processor/adaptiveheadsampler"
	"github.com/deepaucksharma-nr/phoenix-core/internal/processor/reservoirsampler"
	"github.com/deepaucksharma-nr/phoenix-core/internal/processor/topnprocfilter"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/otelcol"
)

func main() {
	// The components are registered via init() in each package
	factories, err := otelcol.Components()
	if err != nil {
		panic(err)
	}

	info := component.BuildInfo{
		Command:     "pte-collector",
		Description: "Phoenix Telemetry Edge Collector",
		Version:     "v0.1.0",
	}

	if err = otelcol.Run(otelcol.Settings{
		Factories: factories,
		BuildInfo: info,
	}); err != nil {
		panic(err)
	}
}

// Import custom components to trigger their init() functions
var (
	_ = pidcontroller.NewFactory
	_ = adaptiveheadsampler.NewFactory
	_ = reservoirsampler.NewFactory
	_ = topnprocfilter.NewFactory
)