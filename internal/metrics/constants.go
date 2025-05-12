package metrics

// Metric Names - using the "pte_" prefix for all custom metrics
const (
	// Trace adaptive head sampler metrics
	MetricPTETraceHeadSamplingProbability = "pte_sampling_trace_head_probability"
	MetricPTETraceHeadSamplingThroughput  = "pte_sampling_trace_head_throughput"
	MetricPTETraceHeadSamplingRejected    = "pte_sampling_trace_head_rejected_total"

	// Legacy names for backward compatibility
	MetricPTEHeadSamplingProbability = "pte_sampling_head_probability" // Deprecated: use MetricPTETraceHeadSamplingProbability
	MetricPTEHeadSamplingThroughput  = "pte_sampling_head_throughput"  // Deprecated: use MetricPTETraceHeadSamplingThroughput
	MetricPTEHeadSamplingRejected    = "pte_sampling_head_rejected_total" // Deprecated: use MetricPTETraceHeadSamplingRejected

	// Log adaptive head sampler metrics
	MetricPTELogHeadSamplingProbability = "pte_sampling_log_head_probability"
	MetricPTELogHeadSamplingThroughput  = "pte_sampling_log_head_throughput"
	MetricPTELogHeadSamplingRejected    = "pte_sampling_log_head_rejected_total"

	// Log content filter metrics
	MetricPTELogSamplingContentFilterMatches  = "pte_sampling_log_content_filter_matches_total"
	MetricPTELogSamplingContentFilterIncludes = "pte_sampling_log_content_filter_includes_total"
	MetricPTELogSamplingContentFilterExcludes = "pte_sampling_log_content_filter_excludes_total"
	MetricPTELogSamplingContentFilterWeighted = "pte_sampling_log_content_filter_weighted_total"

	// Critical log metrics
	MetricPTELogSamplingCriticalMatches       = "pte_sampling_log_critical_matches_total"
	MetricPTELogSamplingCriticalByLevel       = "pte_sampling_log_critical_by_level_total"
	
	// Reservoir sampler metrics
	MetricPTEReservoirSize              = "pte_reservoir_size"
	MetricPTEReservoirWindowTotal       = "pte_reservoir_window_total"
	MetricPTEReservoirCheckpointAge     = "pte_reservoir_checkpoint_age_seconds"
	MetricPTEReservoirLRUEvictions      = "pte_reservoir_lru_evictions_total"
	MetricPTEReservoirFlushDuration     = "pte_reservoir_flush_duration_seconds"
	MetricPTEReservoirCheckpointDuration = "pte_reservoir_checkpoint_duration_seconds"
MetricPTEReservoirDbSizeBytes       = "pte_reservoir_db_size_bytes"
MetricPTEReservoirCompactionCount   = "pte_reservoir_compaction_count_total"
MetricPTEReservoirCompactionDuration = "pte_reservoir_compaction_duration_seconds"
	
	// Process Top-N filter metrics
	MetricPTEProcessTopN           = "pte_process_topn_value"
	MetricPTEProcessTotal          = "pte_process_total"
	MetricPTEProcessFiltered       = "pte_process_filtered_total"
	MetricPTEProcessThresholdCpu   = "pte_process_threshold_cpu"
	MetricPTEProcessThresholdMemory = "pte_process_threshold_memory"
	
	// PID controller metrics
	MetricPTEPIDControllerAdjustments = "pte_pid_controller_adjustments_total"
	MetricPTEPIDQueueUtilization     = "pte_pid_queue_utilization"
	MetricPTEPIDAggressiveDropCount  = "pte_pid_aggressive_drop_count_total"
)

// Metric Descriptions
const (
	// Trace adaptive head sampler metric descriptions
	DescPTETraceHeadSamplingProbability = "Current probability value for the trace adaptive head sampler"
	DescPTETraceHeadSamplingThroughput  = "Number of spans processed by the trace head sampler per second"
	DescPTETraceHeadSamplingRejected    = "Total number of spans rejected by the trace head sampler"

	// Legacy descriptions for backward compatibility
	DescPTEHeadSamplingProbability = "Current probability value for the adaptive head sampler"
	DescPTEHeadSamplingThroughput  = "Number of spans processed by the head sampler per second"
	DescPTEHeadSamplingRejected    = "Total number of spans rejected by the head sampler"

	// Log adaptive head sampler metric descriptions
	DescPTELogHeadSamplingProbability = "Current probability value for the log adaptive head sampler"
	DescPTELogHeadSamplingThroughput  = "Number of logs processed by the log head sampler per second"
	DescPTELogHeadSamplingRejected    = "Total number of logs rejected by the log head sampler"

	// Log content filter metric descriptions
	DescPTELogSamplingContentFilterMatches  = "Total number of log records that matched a content filter"
	DescPTELogSamplingContentFilterIncludes = "Total number of log records that were forcibly included by content filters"
	DescPTELogSamplingContentFilterExcludes = "Total number of log records that were forcibly excluded by content filters"
	DescPTELogSamplingContentFilterWeighted = "Total number of log records that used weighted probability for sampling"

	// Critical log metric descriptions
	DescPTELogSamplingCriticalMatches       = "Total number of log records that matched a critical log pattern"
	DescPTELogSamplingCriticalByLevel       = "Total number of critical logs by severity level"
	
	DescPTEReservoirSize              = "Current number of spans in the reservoir"
	DescPTEReservoirWindowTotal       = "Total number of spans processed in the current window"
	DescPTEReservoirCheckpointAge     = "Time in seconds since the last reservoir checkpoint was saved"
	DescPTEReservoirLRUEvictions      = "Total number of spans evicted from the LRU cache"
	DescPTEReservoirFlushDuration     = "Time in seconds taken to flush the reservoir"
	DescPTEReservoirCheckpointDuration = "Time in seconds taken to create a checkpoint"
DescPTEReservoirDbSizeBytes       = "Size of the BoltDB reservoir database in bytes"
DescPTEReservoirCompactionCount   = "Total number of BoltDB compactions performed"
DescPTEReservoirCompactionDuration = "Time in seconds taken to compact the BoltDB database"
	
	DescPTEProcessTopN           = "Current Top-N value for the process filter"
	DescPTEProcessTotal          = "Total number of processes seen"
	DescPTEProcessFiltered       = "Total number of processes filtered out"
	DescPTEProcessThresholdCpu   = "Current CPU threshold for the process filter"
	DescPTEProcessThresholdMemory = "Current memory threshold for the process filter"
	
	DescPTEPIDControllerAdjustments = "Total number of adjustments made by the PID controller"
	DescPTEPIDQueueUtilization     = "Current queue utilization as seen by the PID controller"
	DescPTEPIDAggressiveDropCount  = "Total number of aggressive drops triggered by the PID controller"
)

// Metric Units
const (
	UnitRatio         = "1"
	UnitCount         = "1"
	UnitBytes         = "By"
	UnitSeconds       = "s"
	UnitMilliseconds  = "ms"
	UnitMicroseconds  = "us"
	UnitNanoseconds   = "ns"
	UnitSpans         = "{spans}"
	UnitProcesses     = "{processes}"
)