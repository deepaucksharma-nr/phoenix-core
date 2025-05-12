package integration

import (
	"testing"
	"os"
	"path/filepath"
	"strings"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestProcessorOrder validates that the processors in collector pipelines
// are defined in the correct order for proper behavior
func TestProcessorOrder(t *testing.T) {
	// Load the collector config from the test directory or wherever it's defined
	configData, err := os.ReadFile(filepath.Join("..", "..", "helm", "pte", "ci", "collector.test.yaml"))
	require.NoError(t, err, "Failed to read test collector config")

	// Parse the YAML
	var config map[string]interface{}
	err = yaml.Unmarshal(configData, &config)
	require.NoError(t, err, "Failed to parse collector config YAML")

	// Get the service section
	service, ok := config["service"].(map[string]interface{})
	require.True(t, ok, "Service section not found in collector config")

	// Get the pipelines section
	pipelines, ok := service["pipelines"].(map[string]interface{})
	require.True(t, ok, "Pipelines section not found in service config")

	// Check traces pipeline processor order
	if tracesPipeline, ok := pipelines["traces/default"].(map[string]interface{}); ok {
		processors, ok := tracesPipeline["processors"].([]interface{})
		require.True(t, ok, "Processors not found in traces pipeline")

		// Convert processors to a string slice for easier assertion
		processorsList := make([]string, len(processors))
		for i, p := range processors {
			processorsList[i] = p.(string)
		}

		// Define the required order for traces pipeline
		requiredOrder := []string{
			"memory_limiter",
			"k8sattributes",
			"resource",
		}

		// Ensure the required processors are in order
		assertProcessorsInOrder(t, processorsList, requiredOrder)

		// Ensure adaptive_head_sampler comes before reservoir_sampler
		assertProcessorOrder(t, processorsList, "adaptive_head_sampler", "reservoir_sampler")

		// Ensure batch comes last (before routing if present)
		assert.True(t, isProcessorBeforeOptionalProcessor(processorsList, "batch", "routing"),
			"batch processor should be at the end (before routing if present)")
	}

	// Check logs pipeline processor order if present
	if logsPipeline, ok := pipelines["logs/default"].(map[string]interface{}); ok {
		processors, ok := logsPipeline["processors"].([]interface{})
		require.True(t, ok, "Processors not found in logs pipeline")

		// Convert processors to a string slice for easier assertion
		processorsList := make([]string, len(processors))
		for i, p := range processors {
			processorsList[i] = p.(string)
		}

		// Define the required order for logs pipeline
		requiredOrder := []string{
			"memory_limiter",
			"k8sattributes",
			"resource",
		}

		// Ensure the required processors are in order
		assertProcessorsInOrder(t, processorsList, requiredOrder)

		// Verify that PII transformer comes before adaptive head sampler if present
		if hasProcessor(processorsList, "transform/pii") {
			assertProcessorOrder(t, processorsList, "transform/pii", "adaptive_head_sampler")
		}

		// Ensure batch comes last (before routing if present)
		assert.True(t, isProcessorBeforeOptionalProcessor(processorsList, "batch", "routing"),
			"batch processor should be at the end (before routing if present)")
	}

	// Check host process metrics pipeline processor order if present
	if metricsPipeline, ok := pipelines["metrics/hostprocesses"].(map[string]interface{}); ok {
		processors, ok := metricsPipeline["processors"].([]interface{})
		require.True(t, ok, "Processors not found in host process metrics pipeline")

		// Convert processors to a string slice for easier assertion
		processorsList := make([]string, len(processors))
		for i, p := range processors {
			processorsList[i] = p.(string)
		}

		// Define the required order for metrics pipeline
		requiredOrder := []string{
			"memory_limiter",
			"k8sattributes",
			"resource",
		}

		// Check basic processor order
		assertProcessorsInOrder(t, processorsList, requiredOrder)

		// Check process-specific processor order
		// 1. fallback_proc_parser should be before transform/process_normalize
		assertProcessorOrder(t, processorsList, "fallback_proc_parser", "transform/process_normalize")
		
		// 2. transform/process_normalize should be before topn_process_metrics_filter
		assertProcessorOrder(t, processorsList, "transform/process_normalize", "topn_process_metrics_filter")
		
		// 3. topn_process_metrics_filter should be before transform/drop_pid_attr
		assertProcessorOrder(t, processorsList, "topn_process_metrics_filter", "transform/drop_pid_attr")
		
		// 4. transform/drop_pid_attr should be before batch
		assertProcessorOrder(t, processorsList, "transform/drop_pid_attr", "batch")

		// Ensure batch comes last (before routing if present)
		assert.True(t, isProcessorBeforeOptionalProcessor(processorsList, "batch", "routing"),
			"batch processor should be at the end (before routing if present)")
	}
}

// assertProcessorsInOrder checks that the processors in requiredOrder appear in the same order
// in the actual processors list (though other processors may be between them)
func assertProcessorsInOrder(t *testing.T, actual []string, required []string) {
	var lastIndex = -1
	for _, req := range required {
		found := false
		for i, act := range actual {
			if act == req {
				assert.Greater(t, i, lastIndex, 
					"Processor %s should come after %s", req, required[lastIndex-1])
				lastIndex = i
				found = true
				break
			}
		}
		assert.True(t, found, "Required processor %s not found in pipeline", req)
	}
}

// assertProcessorOrder checks that processor1 comes before processor2 in the list
func assertProcessorOrder(t *testing.T, processors []string, processor1, processor2 string) {
	idx1 := -1
	idx2 := -1
	
	for i, p := range processors {
		if p == processor1 {
			idx1 = i
		} else if p == processor2 {
			idx2 = i
		}
	}
	
	assert.NotEqual(t, -1, idx1, "Processor %s not found in pipeline", processor1)
	assert.NotEqual(t, -1, idx2, "Processor %s not found in pipeline", processor2)
	assert.Less(t, idx1, idx2, "Processor %s should come before %s", processor1, processor2)
}

// isProcessorBeforeOptionalProcessor checks that processor1 comes before processor2 if processor2 exists
func isProcessorBeforeOptionalProcessor(processors []string, processor1, optionalProcessor2 string) bool {
	idx1 := -1
	idx2 := -1

	for i, p := range processors {
		if p == processor1 {
			idx1 = i
		} else if p == optionalProcessor2 {
			idx2 = i
		}
	}

	// If processor2 is not present, just check processor1 exists
	if idx2 == -1 {
		return idx1 != -1
	}

	// Both exist, check the order
	return idx1 != -1 && idx1 < idx2
}

// hasProcessor checks if a processor exists in the processors list
func hasProcessor(processors []string, processorName string) bool {
	for _, p := range processors {
		if p == processorName {
			return true
		}
	}
	return false
}