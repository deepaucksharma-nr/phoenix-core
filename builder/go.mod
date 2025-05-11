module github.com/deepaucksharma-nr/phoenix-core

go 1.22

require (
	github.com/newrelic/newrelic-opentelemetry-collector v1.20.0
	github.com/open-telemetry/opentelemetry-collector-contrib/extension/healthcheckextension v0.94.0
	github.com/open-telemetry/opentelemetry-collector-contrib/extension/pprofextension v0.94.0
	github.com/open-telemetry/opentelemetry-collector-contrib/extension/prometheusextension v0.94.0
	github.com/open-telemetry/opentelemetry-collector-contrib/extension/storage/filestorage v0.94.0
	github.com/open-telemetry/opentelemetry-collector-contrib/extension/zpagesextension v0.94.0
	github.com/open-telemetry/opentelemetry-collector-contrib/processor/batchprocessor v0.94.0
	github.com/open-telemetry/opentelemetry-collector-contrib/processor/k8sattributesprocessor v0.94.0
	github.com/open-telemetry/opentelemetry-collector-contrib/processor/memorylimiterprocessor v0.94.0
	github.com/open-telemetry/opentelemetry-collector-contrib/processor/resourceprocessor v0.94.0
	github.com/open-telemetry/opentelemetry-collector-contrib/processor/transformprocessor v0.94.0
	github.com/open-telemetry/opentelemetry-collector-contrib/receiver/otlpreceiver v0.94.0
	go.opentelemetry.io/collector v0.94.0
)

// Replace directives for custom components will be added once implemented