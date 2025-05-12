#!/usr/bin/env python3

import os
import unittest
import tempfile
import time
from framework import PTETestFramework

class FallbackProcessParserTests(unittest.TestCase):
    """End-to-End tests for fallback process parser functionality"""

    def setUp(self):
        """Set up test environment for each test"""
        # Create a framework instance
        self.framework = PTETestFramework()

        # Define the test-specific collector configuration
        self.test_config = """
processors:
  fallback_proc_parser:
    enabled: true
    attributes_to_fetch: ["command_line", "owner", "io", "executable_path"]
    strict_mode: false
    critical_attributes: ["process.executable.name", "process.command_line"]
    cache_ttl_seconds: 60
"""

    def test_executable_path_enrichment(self):
        """Test IT-C4: fallback_proc_parser Integration for executable path enrichment"""
        # Create a test process metrics generator that deliberately omits executable path
        test_process_metrics = self.create_test_process_metrics(
            pid=os.getpid(),  # Use our actual PID for realistic test
            include_exec_path=False,
            include_cmd_line=True
        )

        # Deploy PTE with fallback_proc_parser enabled and configured
        with self.framework.deploy_pte(
            custom_config=self.test_config,
            test_name="fallback_proc_parser_integration"
        ) as pte:
            # Send the process metrics with missing executable path
            pte.send_metrics(test_process_metrics)
            
            # Wait for processing
            time.sleep(2)
            
            # Check metrics to see if process was enriched
            metrics = pte.get_metrics()
            
            # Look for enriched PID counter
            enriched_counter = metrics.get("pte_fallback_parser_enriched_pid_total", 0)
            self.assertGreater(enriched_counter, 0, 
                              "Expected at least one process to be enriched")
            
            # Check cache hit metrics after first lookup
            # Send the same metrics again
            pte.send_metrics(test_process_metrics)
            time.sleep(1)
            
            # Get updated metrics
            metrics = pte.get_metrics()
            cache_hits = metrics.get("pte_fallback_parser_cache_hit_total", 0)
            self.assertGreater(cache_hits, 0, 
                              "Expected cache hits after second lookup")

    def test_metrics_collection(self):
        """Test performance metrics collection for fallback_proc_parser"""
        # Deploy PTE with fallback_proc_parser enabled
        with self.framework.deploy_pte(
            custom_config=self.test_config,
            test_name="fallback_proc_parser_metrics"
        ) as pte:
            # Send process metrics to trigger attribute resolution
            test_metrics = self.create_test_process_metrics(
                pid=os.getpid(),
                include_exec_path=False,
                include_cmd_line=False
            )
            
            # Send multiple times to get some cache hits
            for _ in range(3):
                pte.send_metrics(test_metrics)
                time.sleep(1)
            
            # Get metrics
            metrics = pte.get_metrics()
            
            # Check all expected metrics are present
            expected_metrics = [
                "pte_fallback_parser_enriched_pid_total",
                "pte_fallback_parser_cache_hit_total",
                "pte_fallback_parser_cache_miss_total",
                "pte_fallback_parser_attribute_resolution_total",
                "pte_fallback_parser_fetch_duration_ms"
            ]
            
            for metric in expected_metrics:
                self.assertIn(metric, metrics, f"Expected metric {metric} not found")
            
            # Check for performance metrics with detailed labels
            # Specific implementation depends on the labels available in the metrics API
    
    def create_test_process_metrics(self, pid, include_exec_path=False, include_cmd_line=True):
        """Create test process metrics with configurable attributes"""
        # Implementation will depend on the specific format needed by the framework
        # This is a placeholder for the actual implementation
        metrics = {
            "resource": {
                "attributes": {
                    "process.pid": str(pid),
                    "process.executable.name": "test-process"
                }
            },
            "metrics": [
                {
                    "name": "process.cpu.time",
                    "data_type": "Sum",
                    "value": 12345.0
                }
            ]
        }
        
        if include_cmd_line:
            metrics["resource"]["attributes"]["process.command_line"] = "./test-process --test"
            
        if include_exec_path:
            metrics["resource"]["attributes"]["process.executable.path"] = "/usr/bin/test-process"
            
        return metrics


if __name__ == "__main__":
    unittest.main()