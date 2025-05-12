# BoltDB Compaction in Reservoir Sampler

## Overview

The Reservoir Sampler processor now supports scheduled BoltDB compaction. This feature helps manage the size of the BoltDB database used for checkpoint storage, preventing excessive disk space usage over time.

## Configuration

The BoltDB compaction feature is configured through the following settings in the Reservoir Sampler processor configuration:

```yaml
processors:
  reservoir_sampler:
    # Existing configuration
    size_k: 1000
    window_duration: 60s
    checkpoint_path: /data/pte/reservoir_state.db
    checkpoint_interval: 10s

    # New compaction configuration
    db_compaction_schedule_cron: "0 2 * * 0"  # Run at 2:00 AM every Sunday
    db_compaction_target_size: 10485760       # 10MB target size
```

### Configuration Options

| Option | Type | Description | Default |
| ------ | ---- | ----------- | ------- |
| `db_compaction_schedule_cron` | string | A cron expression that specifies when to run BoltDB compaction. Uses standard cron format. When empty, no automatic compaction will be performed. | `"0 2 * * 0"` (2:00 AM every Sunday) |
| `db_compaction_target_size` | integer | The target size threshold for compaction in bytes. If the database exceeds this size, compaction will be performed. | `10485760` (10MB) |

### Cron Expression Format

The `db_compaction_schedule_cron` option uses standard cron format with five fields:

```
┌───────────── minute (0-59)
│ ┌───────────── hour (0-23)
│ │ ┌───────────── day of the month (1-31)
│ │ │ ┌───────────── month (1-12)
│ │ │ │ ┌───────────── day of the week (0-6) (Sunday to Saturday)
│ │ │ │ │
│ │ │ │ │
* * * * *
```

For example:
- `0 2 * * 0` - Run at 2:00 AM every Sunday
- `0 4 * * *` - Run at 4:00 AM every day
- `0 0 1 * *` - Run at midnight on the first day of each month

## How Compaction Works

### Scheduled Compaction

1. At the scheduled time defined by the cron expression, the processor checks the current size of the BoltDB database.
2. If the database size exceeds the target size (`db_compaction_target_size`), compaction is performed.
3. During compaction:
   - The database is closed
   - A new compacted copy of the database is created
   - The original database is replaced with the compacted version
   - The database is reopened
   - Required buckets are recreated if necessary

If compaction is not needed (database size is below the threshold), the process is skipped until the next scheduled time.

### On-Demand Compaction

In addition to scheduled compaction, you can also trigger compaction on demand through the Tunable interface:

```go
// Example: Using the TunableRegistry
registry := tunableregistry.GetInstance()
registry.SetValue("reservoir_sampler", "trigger_compaction", 1.0)
```

The on-demand compaction:
1. Performs the same disk space verification as scheduled compaction
2. Only compacts if the database size exceeds the target size
3. Executes asynchronously to avoid blocking the caller
4. Logs success or failure messages for tracking

## Metrics

The following metrics are reported to track compaction performance:

| Metric | Type | Description |
| ------ | ---- | ----------- |
| `pte_reservoir_db_size_bytes` | Gauge | Current size of the BoltDB database in bytes |
| `pte_reservoir_compaction_count_total` | Counter | Total number of database compactions performed |
| `pte_reservoir_compaction_duration_seconds` | Histogram | Time in seconds taken to complete database compaction |

## Troubleshooting

If compaction is not working as expected, check the logs for the following information:

- Confirmation of scheduler setup: `"Setting up BoltDB compaction scheduler"`
- Successful scheduler startup: `"BoltDB compaction scheduler started"`
- Compaction events: `"BoltDB compaction completed"`
- Compaction errors: `"Scheduled BoltDB compaction failed"`

Common issues include:

1. **Invalid cron expression**:
   - Error: `"invalid db_compaction_schedule_cron format"`
   - Solution: Verify your cron expression using a cron validator

2. **Insufficient permissions**:
   - Error: `"failed to close database before compaction"` or `"failed to replace database with compacted version"`
   - Solution: Ensure the service has proper permissions to read/write the database file

3. **Disk space issues**:
   - Error: `"failed to compact database"`
   - Solution: Ensure sufficient disk space is available for temporary files during compaction

4. **Database in use**:
   - Error: `"database is locked"` or similar
   - Solution: This may indicate multiple processes attempting to access the database simultaneously