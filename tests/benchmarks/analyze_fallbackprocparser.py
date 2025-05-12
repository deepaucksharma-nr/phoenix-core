#!/usr/bin/env python3
"""
Fallback Process Parser Benchmark Analysis

This script analyzes benchmark results from the fallback_proc_parser benchmarks.
It parses the output of 'go test -bench' and generates reports and visualizations
to help understand the performance characteristics of the parser.
"""

import argparse
import re
import pandas as pd
import matplotlib.pyplot as plt
import numpy as np
import os
import json
from datetime import datetime

# Regular expression to parse Go benchmark output
BENCHMARK_PATTERN = r'Benchmark([^\s]+)-\d+\s+(\d+)\s+([\d\.]+)\s+ns/op(?:\s+(\d+)\s+B/op)?(?:\s+(\d+)\s+allocs/op)?'

def parse_benchmark_output(output_text):
    """
    Parse the output of 'go test -bench' command
    """
    results = []
    for line in output_text.splitlines():
        match = re.match(BENCHMARK_PATTERN, line)
        if match:
            benchmark = match.group(1)
            iterations = int(match.group(2))
            ns_per_op = float(match.group(3))
            bytes_per_op = int(match.group(4)) if match.group(4) else None
            allocs_per_op = int(match.group(5)) if match.group(5) else None
            
            # Extract sub-benchmark details if present
            cache_size_match = re.search(r'Size_(\d+)_HitRatio_([\d\.]+)', benchmark)
            if cache_size_match:
                cache_size = int(cache_size_match.group(1))
                hit_ratio = float(cache_size_match.group(2))
                # Get the base benchmark name
                base_name = benchmark.split("Size_")[0]
                results.append({
                    'benchmark': base_name,
                    'cache_size': cache_size,
                    'hit_ratio': hit_ratio,
                    'iterations': iterations,
                    'ns_per_op': ns_per_op,
                    'bytes_per_op': bytes_per_op,
                    'allocs_per_op': allocs_per_op
                })
            else:
                results.append({
                    'benchmark': benchmark,
                    'iterations': iterations,
                    'ns_per_op': ns_per_op,
                    'bytes_per_op': bytes_per_op,
                    'allocs_per_op': allocs_per_op
                })
    
    return results

def create_cache_efficiency_plots(df, output_dir):
    """
    Create plots to visualize cache efficiency under different loads
    """
    # Filter for cache efficiency benchmarks
    cache_df = df[df['benchmark'].str.contains('CacheEfficiency')]
    
    if cache_df.empty:
        print("No cache efficiency benchmark data found")
        return
    
    # Create output directory if it doesn't exist
    os.makedirs(output_dir, exist_ok=True)
    
    # Plot by cache type
    cache_types = cache_df['benchmark'].unique()
    
    for cache_type in cache_types:
        type_df = cache_df[cache_df['benchmark'] == cache_type]
        
        # Plot performance by cache size and hit ratio
        plt.figure(figsize=(10, 6))
        
        # Get unique cache sizes and hit ratios for grouping
        sizes = sorted(type_df['cache_size'].unique())
        ratios = sorted(type_df['hit_ratio'].unique(), reverse=True)
        
        # Create grouped bar chart
        bar_width = 0.8 / len(ratios)
        x = np.arange(len(sizes))
        
        for i, ratio in enumerate(ratios):
            ratio_df = type_df[type_df['hit_ratio'] == ratio]
            # Calculate average ns/op for each cache size with this hit ratio
            y_values = [ratio_df[ratio_df['cache_size'] == size]['ns_per_op'].mean() for size in sizes]
            plt.bar(x + i*bar_width, y_values, width=bar_width, 
                    label=f'Hit Ratio: {ratio:.0%}')
        
        plt.xlabel('Cache Size (# of entries)')
        plt.ylabel('Time (ns/op)')
        plt.title(f'Performance by Cache Size and Hit Ratio - {cache_type}')
        plt.xticks(x + bar_width*(len(ratios)-1)/2, sizes)
        plt.legend()
        plt.grid(True, linestyle='--', alpha=0.7)
        
        # Save the plot
        plt.savefig(os.path.join(output_dir, f'{cache_type}_cache_efficiency.png'))
        plt.close()
    
    # Create a combined plot showing comparison between cache types
    plt.figure(figsize=(12, 8))
    
    # For each cache type, plot the best performance (highest hit ratio)
    for cache_type in cache_types:
        type_df = cache_df[cache_df['benchmark'] == cache_type]
        best_ratio = type_df['hit_ratio'].max()
        best_df = type_df[type_df['hit_ratio'] == best_ratio]
        
        plt.plot(best_df['cache_size'], best_df['ns_per_op'], 
                 marker='o', label=f'{cache_type} (Hit Ratio: {best_ratio:.0%})')
    
    plt.xlabel('Cache Size (# of entries)')
    plt.ylabel('Time (ns/op)')
    plt.title('Cache Performance Comparison - Best Hit Ratio')
    plt.legend()
    plt.grid(True, linestyle='--', alpha=0.7)
    plt.xscale('log')  # Log scale for better visualization
    
    # Save the plot
    plt.savefig(os.path.join(output_dir, 'cache_type_comparison.png'))
    plt.close()

def create_operations_comparison_plot(df, output_dir):
    """
    Create plot comparing different operations (command line, username, exec path)
    """
    # Filter for the main benchmark operations
    ops_df = df[df['benchmark'].isin(['ExecutablePathResolution', 
                                       'ExecutablePathResolutionWithCache',
                                       'UsernameResolution',
                                       'OwnerResolutionWithCache'])]
    
    if ops_df.empty:
        print("No operations benchmark data found")
        return
    
    # Create output directory if it doesn't exist
    os.makedirs(output_dir, exist_ok=True)
    
    # Create bar chart
    plt.figure(figsize=(10, 6))
    
    # Sort by performance (slowest to fastest)
    ops_df = ops_df.sort_values(by='ns_per_op', ascending=False)
    
    plt.barh(ops_df['benchmark'], ops_df['ns_per_op']/1000000, color='skyblue')  # Convert to ms
    plt.xlabel('Time (ms/op)')
    plt.ylabel('Operation')
    plt.title('Fallback Process Parser - Operation Performance Comparison')
    plt.grid(True, linestyle='--', alpha=0.7, axis='x')
    
    # Add value labels
    for i, v in enumerate(ops_df['ns_per_op']/1000000):
        plt.text(v + 0.1, i, f'{v:.2f} ms', va='center')
    
    # Save the plot
    plt.savefig(os.path.join(output_dir, 'operations_comparison.png'))
    plt.close()

def create_full_pipeline_plot(df, output_dir):
    """
    Create plot showing the performance of the full process metrics enrichment pipeline
    """
    # Filter for the process metrics enrichment benchmark
    pipeline_df = df[df['benchmark'] == 'ProcessMetricsEnrichment']
    
    if pipeline_df.empty:
        print("No process metrics enrichment benchmark data found")
        return
    
    # Create output directory if it doesn't exist
    os.makedirs(output_dir, exist_ok=True)
    
    # Create bar chart with error metrics if available
    plt.figure(figsize=(8, 6))
    
    plt.bar(['Process Metrics\nEnrichment'], pipeline_df['ns_per_op']/1000000, color='darkgreen')  # Convert to ms
    plt.ylabel('Time (ms/op)')
    plt.title('Fallback Process Parser - Full Pipeline Performance')
    plt.grid(True, linestyle='--', alpha=0.7, axis='y')
    
    # Add value labels
    plt.text(0, pipeline_df['ns_per_op'].values[0]/1000000 + 0.5, 
             f'{pipeline_df["ns_per_op"].values[0]/1000000:.2f} ms', ha='center')
    
    # Save the plot
    plt.savefig(os.path.join(output_dir, 'full_pipeline_performance.png'))
    plt.close()

def generate_report(results, output_dir):
    """
    Generate a comprehensive performance report with visualizations
    """
    # Convert results to DataFrame for easier analysis
    df = pd.DataFrame(results)
    
    # Create output directory if it doesn't exist
    os.makedirs(output_dir, exist_ok=True)
    
    # Generate plots
    create_cache_efficiency_plots(df, output_dir)
    create_operations_comparison_plot(df, output_dir)
    create_full_pipeline_plot(df, output_dir)
    
    # Generate summary statistics
    summary = {
        'timestamp': datetime.now().isoformat(),
        'total_benchmarks': len(df),
        'benchmark_groups': df['benchmark'].nunique(),
        'fastest_operation': df.loc[df['ns_per_op'].idxmin()]['benchmark'],
        'fastest_time_ns': df['ns_per_op'].min(),
        'slowest_operation': df.loc[df['ns_per_op'].idxmax()]['benchmark'],
        'slowest_time_ns': df['ns_per_op'].max(),
        'average_time_ns': df['ns_per_op'].mean(),
        'median_time_ns': df['ns_per_op'].median()
    }
    
    # Save summary as JSON
    with open(os.path.join(output_dir, 'summary.json'), 'w') as f:
        json.dump(summary, f, indent=2)
    
    # Save full results as CSV
    df.to_csv(os.path.join(output_dir, 'benchmark_results.csv'), index=False)
    
    # Generate HTML report
    html_report = f"""
    <!DOCTYPE html>
    <html>
    <head>
        <title>Fallback Process Parser Benchmark Report</title>
        <style>
            body {{ font-family: Arial, sans-serif; margin: 20px; }}
            h1, h2 {{ color: #333; }}
            table {{ border-collapse: collapse; width: 100%; margin-bottom: 20px; }}
            th, td {{ border: 1px solid #ddd; padding: 8px; text-align: left; }}
            th {{ background-color: #f2f2f2; }}
            tr:nth-child(even) {{ background-color: #f9f9f9; }}
            .container {{ display: flex; flex-wrap: wrap; }}
            .image-container {{ margin: 10px; }}
            img {{ max-width: 600px; border: 1px solid #ddd; }}
        </style>
    </head>
    <body>
        <h1>Fallback Process Parser Benchmark Report</h1>
        <p>Generated on: {summary['timestamp']}</p>
        
        <h2>Summary</h2>
        <table>
            <tr><th>Metric</th><th>Value</th></tr>
            <tr><td>Total Benchmarks</td><td>{summary['total_benchmarks']}</td></tr>
            <tr><td>Benchmark Groups</td><td>{summary['benchmark_groups']}</td></tr>
            <tr><td>Fastest Operation</td><td>{summary['fastest_operation']}</td></tr>
            <tr><td>Fastest Time</td><td>{summary['fastest_time_ns']:.2f} ns/op ({summary['fastest_time_ns']/1000000:.4f} ms/op)</td></tr>
            <tr><td>Slowest Operation</td><td>{summary['slowest_operation']}</td></tr>
            <tr><td>Slowest Time</td><td>{summary['slowest_time_ns']:.2f} ns/op ({summary['slowest_time_ns']/1000000:.4f} ms/op)</td></tr>
            <tr><td>Average Time</td><td>{summary['average_time_ns']:.2f} ns/op ({summary['average_time_ns']/1000000:.4f} ms/op)</td></tr>
            <tr><td>Median Time</td><td>{summary['median_time_ns']:.2f} ns/op ({summary['median_time_ns']/1000000:.4f} ms/op)</td></tr>
        </table>
        
        <h2>Cache Efficiency Analysis</h2>
        <div class="container">
            <div class="image-container">
                <h3>Cache Types Comparison</h3>
                <img src="cache_type_comparison.png" alt="Cache Types Comparison">
            </div>
    """
    
    # Add individual cache type plots
    cache_types = [b for b in df['benchmark'].unique() if 'CacheEfficiency' in b]
    for cache_type in cache_types:
        if os.path.exists(os.path.join(output_dir, f'{cache_type}_cache_efficiency.png')):
            html_report += f"""
            <div class="image-container">
                <h3>{cache_type}</h3>
                <img src="{cache_type}_cache_efficiency.png" alt="{cache_type} Cache Efficiency">
            </div>
            """
    
    html_report += """
        </div>
        
        <h2>Operations Performance Comparison</h2>
        <div class="container">
            <div class="image-container">
                <img src="operations_comparison.png" alt="Operations Performance Comparison">
            </div>
        </div>
        
        <h2>Full Pipeline Performance</h2>
        <div class="container">
            <div class="image-container">
                <img src="full_pipeline_performance.png" alt="Full Pipeline Performance">
            </div>
        </div>
        
        <h2>Raw Benchmark Results</h2>
        <table>
            <tr>
                <th>Benchmark</th>
                <th>Iterations</th>
                <th>Time (ns/op)</th>
                <th>Time (ms/op)</th>
                <th>Memory (B/op)</th>
                <th>Allocations (allocs/op)</th>
            </tr>
    """
    
    # Add all benchmark results
    for _, row in df.iterrows():
        bytes_per_op = row['bytes_per_op'] if pd.notna(row['bytes_per_op']) else 'N/A'
        allocs_per_op = row['allocs_per_op'] if pd.notna(row['allocs_per_op']) else 'N/A'
        html_report += f"""
            <tr>
                <td>{row['benchmark']}</td>
                <td>{row['iterations']}</td>
                <td>{row['ns_per_op']:.2f}</td>
                <td>{row['ns_per_op']/1000000:.4f}</td>
                <td>{bytes_per_op}</td>
                <td>{allocs_per_op}</td>
            </tr>
        """
    
    html_report += """
        </table>
    </body>
    </html>
    """
    
    # Save HTML report
    with open(os.path.join(output_dir, 'benchmark_report.html'), 'w') as f:
        f.write(html_report)
    
    print(f"Report generated in {output_dir}")

def main():
    parser = argparse.ArgumentParser(description='Analyze fallback process parser benchmarks')
    parser.add_argument('--input', '-i', required=True, help='Benchmark output file')
    parser.add_argument('--output', '-o', default='benchmark_report', help='Output directory for report')
    
    args = parser.parse_args()
    
    # Read benchmark output
    with open(args.input, 'r') as f:
        benchmark_output = f.read()
    
    # Parse results
    results = parse_benchmark_output(benchmark_output)
    
    # Generate report
    generate_report(results, args.output)

if __name__ == '__main__':
    main()