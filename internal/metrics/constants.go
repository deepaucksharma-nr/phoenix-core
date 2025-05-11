package metrics

// Metric Names - using the "pte_" prefix for all custom metrics
const (
	// Adaptive head sampler metrics
	MetricPTEHeadSamplingProbability = "pte_sampling_head_probability"
	MetricPTEHeadSamplingThroughput  = "pte_sampling_head_throughput"
	MetricPTEHeadSamplingRejected    = "pte_sampling_head_rejected_total"
	
	// Reservoir sampler metrics
	MetricPTEReservoirSize              = "pte_reservoir_size"
	MetricPTEReservoirWindowTotal       = "pte_reservoir_window_total"
	MetricPTEReservoirCheckpointAge     = "pte_reservoir_checkpoint_age_seconds"
	MetricPTEReservoirLRUEvictions      = "pte_reservoir_lru_evictions_total"
	MetricPTEReservoirFlushDuration     = "pte_reservoir_flush_duration_seconds"
	MetricPTEReservoirCheckpointDuration = "pte_reservoir_checkpoint_duration_seconds"
	
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
	DescPTEHeadSamplingProbability = "Current probability value for the adaptive head sampler"
	DescPTEHeadSamplingThroughput  = "Number of spans processed by the head sampler per second"
	DescPTEHeadSamplingRejected    = "Total number of spans rejected by the head sampler"
	
	DescPTEReservoirSize              = "Current number of spans in the reservoir"
	DescPTEReservoirWindowTotal       = "Total number of spans processed in the current window"
	DescPTEReservoirCheckpointAge     = "Time in seconds since the last reservoir checkpoint was saved"
	DescPTEReservoirLRUEvictions      = "Total number of spans evicted from the LRU cache"
	DescPTEReservoirFlushDuration     = "Time in seconds taken to flush the reservoir"
	DescPTEReservoirCheckpointDuration = "Time in seconds taken to create a checkpoint"
	
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