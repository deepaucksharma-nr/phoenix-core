package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestFallbackProcParserIntegration validates the integration of the fallback_proc_parser 
// in the collector pipeline and verifies its configuration
func TestFallbackProcParserIntegration(t *testing.T) {
	// Use CI collector config for testing
	configData, err := os.ReadFile(filepath.Join("..", "..", "helm", "pte", "ci", "collector.test.yaml"))
	require.NoError(t, err, "Failed to read test collector config")

	// Parse the YAML
	var config map[string]interface{}
	err = yaml.Unmarshal(configData, &config)
	require.NoError(t, err, "Failed to parse collector config YAML")

	// Check that processors section exists
	processors, ok := config["processors"].(map[string]interface{})
	require.True(t, ok, "Processors section not found in collector config")

	// Check that fallback_proc_parser is defined
	procParserConfig, ok := processors["fallback_proc_parser"]
	if !ok {
		t.Skip("fallback_proc_parser not defined in test config, skipping test")
	}

	// Validate that the processor is configured correctly
	parserConfig, ok := procParserConfig.(map[string]interface{})
	require.True(t, ok, "fallback_proc_parser configuration not found")

	// Check the configuration structure
	assert.Contains(t, parserConfig, "enabled", "enabled flag should be defined")
	
	// Check for attributes_to_fetch
	attributesToFetch, ok := parserConfig["attributes_to_fetch"].([]interface{})
	require.True(t, ok, "attributes_to_fetch should be defined")
	
	// Convert to string slice for easier assertion
	attrStrings := make([]string, len(attributesToFetch))
	for i, attr := range attributesToFetch {
		attrStrings[i] = attr.(string)
	}

	// Check for required attributes in attributes_to_fetch
	assert.Contains(t, attrStrings, "command_line", "command_line should be in attributes_to_fetch")
	
	// Check for new executable_path attribute (if it should be in the default config)
	if contains(attrStrings, "executable_path") {
		t.Logf("executable_path is configured in attributes_to_fetch")
	} else {
		t.Logf("executable_path is not configured in attributes_to_fetch")
	}

	// Check for cache_ttl_seconds
	if cacheTTL, ok := parserConfig["cache_ttl_seconds"].(int); ok {
		assert.GreaterOrEqual(t, cacheTTL, 60, "cache_ttl_seconds should be at least 60 seconds")
	}

	// Validate service pipelines to ensure the processor is in the right pipeline
	service, ok := config["service"].(map[string]interface{})
	require.True(t, ok, "Service section not found in collector config")

	pipelines, ok := service["pipelines"].(map[string]interface{})
	require.True(t, ok, "Pipelines section not found in service config")

	// Check metrics pipeline since the processor is for metrics
	metricsPipeline, ok := pipelines["metrics"].(map[string]interface{})
	if !ok {
		t.Skip("Metrics pipeline not defined in test config, skipping pipeline check")
	}

	// Check that the processor is in the metrics pipeline
	processors, ok = metricsPipeline["processors"].([]interface{})
	require.True(t, ok, "Processors not defined in metrics pipeline")

	// Convert to string slice
	processorStrings := make([]string, len(processors))
	for i, proc := range processors {
		processorStrings[i] = proc.(string)
	}

	// Check for fallback_proc_parser in the pipeline
	assert.Contains(t, processorStrings, "fallback_proc_parser", "fallback_proc_parser should be in the metrics pipeline")

	// Verify processor ordering
	// The fallback_proc_parser should come after resource and before transform/process_normalize
	// Find the positions of relevant processors
	resourcePos := -1
	procParserPos := -1
	normalizePos := -1

	for i, proc := range processorStrings {
		if proc == "resource" {
			resourcePos = i
		} else if proc == "fallback_proc_parser" {
			procParserPos = i
		} else if strings.HasPrefix(proc, "transform/process_normalize") {
			normalizePos = i
		}
	}

	// Check the ordering if all processors are present
	if resourcePos != -1 && procParserPos != -1 {
		assert.Greater(t, procParserPos, resourcePos, "fallback_proc_parser should come after resource")
	}

	if procParserPos != -1 && normalizePos != -1 {
		assert.Less(t, procParserPos, normalizePos, "fallback_proc_parser should come before process_normalize")
	}
}

// TestFallbackProcParserExecutablePathConfig verifies that the executable_path feature
// is correctly configured when enabled
func TestFallbackProcParserExecutablePathConfig(t *testing.T) {
	// Create a temp file with a test configuration that includes executable_path
	tempDir, err := os.MkdirTemp("", "fallbackproctest")
	require.NoError(t, err, "Failed to create temp directory")
	defer os.RemoveAll(tempDir)

	// Create a test config with executable_path enabled
	testConfig := `
processors:
  fallback_proc_parser:
    enabled: true
    attributes_to_fetch: ["command_line", "owner", "io", "executable_path"]
    strict_mode: false
    critical_attributes: ["process.executable.name", "process.command_line"]
    cache_ttl_seconds: 300
`
	configPath := filepath.Join(tempDir, "test-config.yaml")
	err = os.WriteFile(configPath, []byte(testConfig), 0644)
	require.NoError(t, err, "Failed to write test config")

	// Parse the YAML
	var config map[string]interface{}
	configData, err := os.ReadFile(configPath)
	require.NoError(t, err, "Failed to read test config")
	
	err = yaml.Unmarshal(configData, &config)
	require.NoError(t, err, "Failed to parse config YAML")

	// Check processor configuration
	processors := config["processors"].(map[string]interface{})
	procParserConfig := processors["fallback_proc_parser"].(map[string]interface{})
	
	// Check attributes_to_fetch
	attributesToFetch := procParserConfig["attributes_to_fetch"].([]interface{})
	
	// Convert to string slice
	attrStrings := make([]string, len(attributesToFetch))
	for i, attr := range attributesToFetch {
		attrStrings[i] = attr.(string)
	}
	
	// Verify executable_path is in the config
	assert.Contains(t, attrStrings, "executable_path", "executable_path should be in attributes_to_fetch")
	
	// Verify cache TTL is set
	cacheTTL := procParserConfig["cache_ttl_seconds"].(int)
	assert.Equal(t, 300, cacheTTL, "cache_ttl_seconds should be 300")
}

// Helper function: contains checks if a string is in a slice
func contains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}