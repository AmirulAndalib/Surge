#!/bin/bash

# Surge Performance Benchmarking Script
# Run performance analysis on download happy path

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
RESULTS_DIR="${PROJECT_ROOT}/perf-results"

# Create results directory
mkdir -p "$RESULTS_DIR"

echo "╔════════════════════════════════════════════════════════════════╗"
echo "║          Surge Download Performance Analysis Suite             ║"
echo "╚════════════════════════════════════════════════════════════════╝"
echo ""

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Function to run benchmark and capture results
run_benchmark() {
    local name=$1
    local pattern=$2
    local timeout=${3:-30}
    
    echo -e "${BLUE}[$(date +'%H:%M:%S')]${NC} Running: $name"
    
    if timeout $timeout go test -bench="$pattern" \
                                -benchtime=5s \
                                -benchmem \
                                -run=^$ \
                                ./internal/engine/concurrent \
                                > "$RESULTS_DIR/${name}.txt" 2>&1; then
        echo -e "${GREEN}✓${NC} $name completed"
        tail -3 "$RESULTS_DIR/${name}.txt"
    else
        echo -e "${RED}✗${NC} $name failed or timed out"
        return 1
    fi
    echo ""
}

# Function to run test and capture output
run_test() {
    local name=$1
    local pattern=$2
    local timeout=${3:-30}
    
    echo -e "${BLUE}[$(date +'%H:%M:%S')]${NC} Running: $name"
    
    if timeout $timeout go test -run="$pattern" -v -timeout=60s \
                                ./internal/engine/concurrent \
                                > "$RESULTS_DIR/${name}.txt" 2>&1; then
        echo -e "${GREEN}✓${NC} $name completed"
        grep -E "^(ok|FAIL|---)" "$RESULTS_DIR/${name}.txt" | tail -5 || true
    else
        echo -e "${RED}✗${NC} $name failed or timed out"
        return 1
    fi
    echo ""
}

# ==============================================================================
# MICRO-BENCHMARKS
# ==============================================================================

echo -e "${YELLOW}━━━ Micro-Benchmarks (Lock-free & Allocation Overhead) ━━━${NC}"
echo ""

run_benchmark "Task Creation Overhead" "Benchmark_TaskCreation" 45
run_benchmark "CAS Contention (Hedging)" "Benchmark_CAS_Contention" 45
run_benchmark "File Sync Performance" "Benchmark_FileSync_Overhead" 30
run_benchmark "Progress Update Batching" "Benchmark_LockContention_ProgressBatching" 60

# ==============================================================================
# INTEGRATION TESTS
# ==============================================================================

echo -e "${YELLOW}━━━ Integration Tests (Real-world Scenarios) ━━━${NC}"
echo ""

run_test "Lock Contention Analysis" "TestLockContention_ProgressUpdates" 60
run_test "Download Profiling" "TestDownloadHappyPath_Profiling" 120

# ==============================================================================
# RESULTS ANALYSIS
# ==============================================================================

echo -e "${YELLOW}━━━ Results Summary ━━━${NC}"
echo ""

echo "Benchmark Files Generated:"
ls -lh "$RESULTS_DIR"/*.txt 2>/dev/null | awk '{print "  " $9 " (" $5 ")"}'
echo ""

# Try to extract key metrics
echo "Key Performance Metrics:"
echo ""

if [ -f "$RESULTS_DIR/Task Creation Overhead.txt" ]; then
    echo -e "${BLUE}Task Creation:${NC}"
    grep "Benchmark_TaskCreation" "$RESULTS_DIR/Task Creation Overhead.txt" | head -3
    echo ""
fi

if [ -f "$RESULTS_DIR/CAS Contention (Hedging).txt" ]; then
    echo -e "${BLUE}CAS Contention:${NC}"
    grep "Benchmark_CAS" "$RESULTS_DIR/CAS Contention (Hedging).txt" | head -3
    echo ""
fi

# ==============================================================================
# COMPARISON WITH BASELINE
# ==============================================================================

BASELINE_FILE="${PROJECT_ROOT}/.performance/baseline.txt"
if [ -f "$BASELINE_FILE" ]; then
    echo -e "${YELLOW}━━━ Comparison with Baseline ━━━${NC}"
    echo ""
    
    # Try to install benchstat if available
    if command -v benchstat &> /dev/null; then
        echo "Running benchstat comparison..."
        # Combine all current results
        cat "$RESULTS_DIR"/*.txt > "$RESULTS_DIR/current.txt" 2>/dev/null || true
        
        if [ -s "$RESULTS_DIR/current.txt" ]; then
            benchstat "$BASELINE_FILE" "$RESULTS_DIR/current.txt" || true
        fi
    else
        echo -e "${YELLOW}Note:${NC} Install 'benchstat' for baseline comparison:"
        echo "  go install golang.org/x/perf/cmd/benchstat@latest"
    fi
    echo ""
fi

# ==============================================================================
# SUMMARY
# ==============================================================================

echo -e "${YELLOW}━━━ Performance Analysis Complete ━━━${NC}"
echo ""
echo "Results saved to: $RESULTS_DIR"
echo ""
echo "Next steps:"
echo "  1. Review results: less $RESULTS_DIR/*.txt"
echo "  2. Save baseline:  cp $RESULTS_DIR/current.txt $BASELINE_FILE"
echo "  3. Compare runs:   benchstat baseline.txt current.txt"
echo ""
echo "For detailed analysis:"
echo "  go test -run=TestDownloadHappyPath_Profiling -cpuprofile=cpu.prof ./internal/engine/concurrent"
echo "  go tool pprof cpu.prof"
echo ""
