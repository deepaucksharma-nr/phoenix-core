package pidcontroller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/deepaucksharma-nr/phoenix-core/internal/metrics"
	"github.com/deepaucksharma-nr/phoenix-core/internal/pkg/tunableregistry"
	"github.com/prometheus/common/expfmt"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.uber.org/zap"
)

type pidControllerExtension struct {
	config          *Config
	logger          *zap.Logger
	ticker          *time.Ticker
	httpClient      *http.Client
	stopCh          chan struct{}
	ewma            atomic.Value // stores float64
	highCount       int
	lowCount        int
	queueSizeRegex  *regexp.Regexp
	queueCapRegex   *regexp.Regexp
	tunableRegistry *tunableregistry.Registry

	// Metrics
	metricsRegistry         *metrics.MetricsRegistry
	metricsTicker           *time.Ticker
	adjustmentCount         atomic.Int64
	aggressiveDropCount     atomic.Int64
	currentQueueUtilization atomic.Value // stores float64
}

// Ensure the extension implements required interfaces
var _ extension.Extension = (*pidControllerExtension)(nil)

func newPIDController(settings extension.CreateSettings, config component.Config) (extension.Extension, error) {
	cfg := config.(*Config)

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Prepare HTTP client for metrics scraping
	httpClient := &http.Client{
		Timeout: 500 * time.Millisecond, // Short timeout for metrics scraping
	}

	// Compile regexes for parsing metrics
	queueSizeRegex := regexp.MustCompile(`otelcol_exporter_queue_size{exporter="([^"]*)"} (\d+)`)
	queueCapRegex := regexp.MustCompile(`otelcol_exporter_queue_capacity{exporter="([^"]*)"} (\d+)`)

	// Create the controller
	controller := &pidControllerExtension{
		config:          cfg,
		logger:          settings.Logger,
		httpClient:      httpClient,
		stopCh:          make(chan struct{}),
		queueSizeRegex:  queueSizeRegex,
		queueCapRegex:   queueCapRegex,
		tunableRegistry: tunableregistry.GetInstance(),
		metricsRegistry: metrics.GetInstance(settings.Logger),
	}

	// Initialize EWMA with 0
	controller.ewma.Store(0.0)
	controller.currentQueueUtilization.Store(0.0)

	// Initialize metrics
	if err := controller.initMetrics(); err != nil {
		return nil, fmt.Errorf("failed to initialize metrics: %w", err)
	}

	// Get the registry ID
	registryID := cfg.TunableRegistryID

	controller.logger.Info("PID controller extension created",
		zap.String("interval", cfg.Interval),
		zap.Float64("target_high", cfg.TargetQueueUtilizationHigh),
		zap.Float64("target_low", cfg.TargetQueueUtilizationLow),
		zap.Float64("adjustment_up", cfg.AdjustmentFactorUp),
		zap.Float64("adjustment_down", cfg.AdjustmentFactorDown),
		zap.Float64("ewma_alpha", cfg.EWMAAlpha),
		zap.Float64("aggressive_drop_factor", cfg.AggressiveDropFactor),
		zap.Int("aggressive_window", cfg.AggressiveDropWindowCount),
		zap.Strings("exporters", cfg.ExporterNames),
		zap.String("tunable_registry_id", registryID))

	return controller, nil
}

// initMetrics initializes the metrics for the PID controller
func (pc *pidControllerExtension) initMetrics() error {
	// Queue utilization gauge
	_, err := pc.metricsRegistry.GetOrCreateGauge(
		metrics.MetricPTEPIDQueueUtilization,
		metrics.DescPTEPIDQueueUtilization,
		metrics.UnitRatio,
	)
	if err != nil {
		return err
	}

	// Adjustments counter
	_, err = pc.metricsRegistry.GetOrCreateCounter(
		metrics.MetricPTEPIDControllerAdjustments,
		metrics.DescPTEPIDControllerAdjustments,
		metrics.UnitCount,
	)
	if err != nil {
		return err
	}

	// Aggressive drop counter
	_, err = pc.metricsRegistry.GetOrCreateCounter(
		metrics.MetricPTEPIDAggressiveDropCount,
		metrics.DescPTEPIDAggressiveDropCount,
		metrics.UnitCount,
	)
	if err != nil {
		return err
	}

	return nil
}

// Start implements the extension.Extension interface.
func (pc *pidControllerExtension) Start(ctx context.Context, host component.Host) error {
	interval, _ := time.ParseDuration(pc.config.Interval)
	pc.ticker = time.NewTicker(interval)

	// Start metrics reporting ticker (every 10 seconds)
	pc.metricsTicker = time.NewTicker(10 * time.Second)

	// Start the control loop
	go pc.controlLoop()

	// Start metrics reporting loop
	go pc.reportMetrics()

	pc.logger.Info("PID controller started with metrics reporting")
	return nil
}

// Shutdown implements the extension.Extension interface.
func (pc *pidControllerExtension) Shutdown(ctx context.Context) error {
	if pc.ticker != nil {
		pc.ticker.Stop()
	}

	if pc.metricsTicker != nil {
		pc.metricsTicker.Stop()
	}

	close(pc.stopCh)
	pc.logger.Info("PID controller stopped")
	return nil
}

// reportMetrics periodically reports metrics
func (pc *pidControllerExtension) reportMetrics() {
	for {
		select {
		case <-pc.metricsTicker.C:
			// Report queue utilization
			utilization := pc.currentQueueUtilization.Load().(float64)
			err := pc.metricsRegistry.UpdateGauge(
				context.Background(),
				metrics.MetricPTEPIDQueueUtilization,
				utilization,
				map[string]string{"extension": "pid_controller"},
			)
			if err != nil {
				pc.logger.Error("Failed to update queue utilization metric", zap.Error(err))
			}

			// Report adjustments and aggressive drops (if any occurred)
			adjustments := pc.adjustmentCount.Swap(0)
			if adjustments > 0 {
				err = pc.metricsRegistry.UpdateCounter(
					context.Background(),
					metrics.MetricPTEPIDControllerAdjustments,
					float64(adjustments),
					map[string]string{"extension": "pid_controller"},
				)
				if err != nil {
					pc.logger.Error("Failed to update adjustments metric", zap.Error(err))
				}
			}

			aggressiveDrops := pc.aggressiveDropCount.Swap(0)
			if aggressiveDrops > 0 {
				err = pc.metricsRegistry.UpdateCounter(
					context.Background(),
					metrics.MetricPTEPIDAggressiveDropCount,
					float64(aggressiveDrops),
					map[string]string{"extension": "pid_controller"},
				)
				if err != nil {
					pc.logger.Error("Failed to update aggressive drops metric", zap.Error(err))
				}
			}

		case <-pc.stopCh:
			return
		}
	}
}

// controlLoop runs the PID control loop
func (pc *pidControllerExtension) controlLoop() {
	for {
		select {
		case <-pc.ticker.C:
			pc.runControlCycle()
		case <-pc.stopCh:
			return
		}
	}
}

// runControlCycle executes a single control cycle
func (pc *pidControllerExtension) runControlCycle() {
	// Fetch metrics from the metrics endpoint
	queueUtilization, err := pc.fetchQueueUtilization()
	if err != nil {
		pc.logger.Error("Failed to fetch queue metrics", zap.Error(err))
		return
	}

	// Store current utilization for metrics reporting
	pc.currentQueueUtilization.Store(queueUtilization)

	// Calculate EWMA of queue utilization
	oldEWMA := pc.ewma.Load().(float64)
	newEWMA := pc.config.EWMAAlpha*queueUtilization + (1-pc.config.EWMAAlpha)*oldEWMA
	pc.ewma.Store(newEWMA)

	// Get the registry ID
	registryID := pc.config.TunableRegistryID

	// Look up the tunable
	tunable, exists := pc.tunableRegistry.Lookup(registryID)
	if !exists {
		pc.logger.Error("Failed to find tunable in registry", zap.String("id", registryID))
		return
	}

	// Get current probability value
	currentP := tunable.GetValue("probability")
	var newP float64
	madeAdjustment := false

	// Apply control logic
	if newEWMA > pc.config.TargetQueueUtilizationHigh {
		// Queue utilization is too high, decrease probability
		pc.highCount++
		pc.lowCount = 0
		madeAdjustment = true

		if pc.highCount >= pc.config.AggressiveDropWindowCount {
			// Sustained high utilization, apply aggressive drop
			newP = currentP * pc.config.AggressiveDropFactor
			pc.logger.Warn("Applying aggressive drop factor due to sustained high queue utilization",
				zap.Float64("utilization", newEWMA),
				zap.Float64("old_p", currentP),
				zap.Float64("new_p", newP),
				zap.Int("high_count", pc.highCount))

			// Track aggressive drop for metrics
			pc.aggressiveDropCount.Add(1)
		} else {
			// Normal high utilization, apply normal adjustment
			newP = currentP * pc.config.AdjustmentFactorDown
			pc.logger.Info("Decreasing sampling probability due to high queue utilization",
				zap.Float64("utilization", newEWMA),
				zap.Float64("old_p", currentP),
				zap.Float64("new_p", newP))
		}
	} else if newEWMA < pc.config.TargetQueueUtilizationLow {
		// Queue utilization is too low, increase probability
		pc.lowCount++
		pc.highCount = 0
		madeAdjustment = true

		newP = currentP * pc.config.AdjustmentFactorUp
		pc.logger.Info("Increasing sampling probability due to low queue utilization",
			zap.Float64("utilization", newEWMA),
			zap.Float64("old_p", currentP),
			zap.Float64("new_p", newP))
	} else {
		// Queue utilization is within desired range, maintain current probability
		pc.highCount = 0
		pc.lowCount = 0
		newP = currentP
		pc.logger.Debug("Queue utilization within desired range, maintaining probability",
			zap.Float64("utilization", newEWMA),
			zap.Float64("probability", currentP))
	}

	// Update the tunable probability value
	tunable.SetValue("probability", newP)

	// Track adjustment for metrics if we made a change
	if madeAdjustment {
		pc.adjustmentCount.Add(1)
	}

	// Reset highCount after applying aggressive drop to prevent immediate re-triggering
	if pc.highCount >= pc.config.AggressiveDropWindowCount {
		pc.highCount = 0
	}
}

// fetchQueueUtilization scrapes metrics and calculates queue utilization
func (pc *pidControllerExtension) fetchQueueUtilization() (float64, error) {
	// Fetch metrics from the endpoint
	resp, err := pc.httpClient.Get(pc.config.MetricsEndpoint)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("metrics endpoint returned status %d", resp.StatusCode)
	}
	
	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	
	// Parse metrics using Prometheus text format parser
	var parser expfmt.TextParser
	metricFamilies, err := parser.TextToMetricFamilies(strings.NewReader(string(body)))
	if err != nil {
		return 0, err
	}
	
	// Look for exporter queue metrics
	var totalSize, totalCapacity float64
	
	// For each configured exporter
	for _, exporterName := range pc.config.ExporterNames {
		// Find queue size
		if queueSizeFam, ok := metricFamilies["otelcol_exporter_queue_size"]; ok {
			for _, m := range queueSizeFam.Metric {
				for _, label := range m.Label {
					if label.GetName() == "exporter" && label.GetValue() == exporterName {
						totalSize += m.Gauge.GetValue()
					}
				}
			}
		}
		
		// Find queue capacity
		if queueCapFam, ok := metricFamilies["otelcol_exporter_queue_capacity"]; ok {
			for _, m := range queueCapFam.Metric {
				for _, label := range m.Label {
					if label.GetName() == "exporter" && label.GetValue() == exporterName {
						totalCapacity += m.Gauge.GetValue()
					}
				}
			}
		}
	}
	
	// Fallback to regex parsing if Prometheus parser didn't work
	if totalCapacity == 0 {
		// Parse queue capacity
		capMatches := pc.queueCapRegex.FindAllStringSubmatch(string(body), -1)
		for _, match := range capMatches {
			if len(match) == 3 {
				exporterName := match[1]
				if contains(pc.config.ExporterNames, exporterName) {
					capacity, _ := strconv.ParseFloat(match[2], 64)
					totalCapacity += capacity
				}
			}
		}
		
		// Parse queue size
		sizeMatches := pc.queueSizeRegex.FindAllStringSubmatch(string(body), -1)
		for _, match := range sizeMatches {
			if len(match) == 3 {
				exporterName := match[1]
				if contains(pc.config.ExporterNames, exporterName) {
					size, _ := strconv.ParseFloat(match[2], 64)
					totalSize += size
				}
			}
		}
	}
	
	// Calculate utilization
	if totalCapacity == 0 {
		// Avoid division by zero
		return 0, fmt.Errorf("queue capacity metrics not found for exporters: %v", pc.config.ExporterNames)
	}
	
	utilization := totalSize / totalCapacity
	
	pc.logger.Debug("Queue metrics scraped",
		zap.Float64("size", totalSize),
		zap.Float64("capacity", totalCapacity),
		zap.Float64("utilization", utilization))
	
	return utilization, nil
}

// Helper function to check if a string slice contains a value
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}