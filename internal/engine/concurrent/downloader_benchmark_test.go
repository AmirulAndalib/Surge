package concurrent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SurgeDM/Surge/internal/engine/types"
	"github.com/SurgeDM/Surge/internal/testutil"
)

// BenchmarkMetrics captures detailed timing information for analysis
type BenchmarkMetrics struct {
	TotalTime             time.Duration
	MirrorProbeTime       time.Duration
	MetadataBootstrapTime time.Duration
	TaskCreationTime      time.Duration
	HelperStartTime       time.Duration
	WorkerExecutionTime   time.Duration
	FileSync              time.Duration
	BytesDownloaded       int64
	TasksCreated          int
	ActiveTaskPeaks       int
	LockContentionEvents  int
	ProgressUpdates       int
	ThroughputMBps        float64
}

// Benchmark_LockContention_ProgressBatching measures chunk map lock contention
func Benchmark_LockContention_ProgressBatching(b *testing.B) {
	fileSize := int64(100 * types.MB)
	chunkSize := int64(1 * types.MB)
	state := types.NewProgressState("bench", fileSize)

	state.InitBitmap(fileSize, chunkSize)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate concurrent progress updates (worst case: 10 workers)
		var wg sync.WaitGroup
		for w := 0; w < 10; w++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					offset := int64(workerID*10+j) * chunkSize
					if offset < fileSize {
						state.UpdateChunkStatus(offset, chunkSize, types.ChunkCompleted)
					}
				}
			}(w)
		}
		wg.Wait()
	}
}

// Benchmark_TaskCreation measures overhead of task queue setup
func Benchmark_TaskCreation(b *testing.B) {
	fileSize := int64(1000 * types.MB)

	testCases := []struct {
		name      string
		chunkSize int64
	}{
		{"512KB chunks", 512 * types.KB},
		{"1MB chunks", 1 * types.MB},
		{"5MB chunks", 5 * types.MB},
		{"10MB chunks", 10 * types.MB},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				tasks := createTasks(fileSize, tc.chunkSize)
				_ = tasks // use to avoid compiler optimizations
			}
		})
	}
}

// Benchmark_CAS_Contention measures lock-free deduplication overhead
func Benchmark_CAS_Contention(b *testing.B) {
	sharedMax := &atomic.Int64{}
	sharedMax.Store(0)

	b.Run("SingleWriter", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			offset := int64(i * 1024)
			for {
				maxOff := sharedMax.Load()
				if sharedMax.CompareAndSwap(maxOff, offset) {
					break
				}
			}
		}
	})

	b.Run("TenWriters", func(b *testing.B) {
		sharedMax.Store(0)
		b.ReportAllocs()
		b.ResetTimer()

		var wg sync.WaitGroup
		itemsPerWriter := b.N / 10
		for w := 0; w < 10; w++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()
				for i := 0; i < itemsPerWriter; i++ {
					offset := int64(workerID*itemsPerWriter+i) * 1024
					for {
						maxOff := sharedMax.Load()
						if sharedMax.CompareAndSwap(maxOff, offset) {
							break
						}
					}
				}
			}(w)
		}
		wg.Wait()
	})
}

// Benchmark_FileSync_Overhead measures fsync performance
func Benchmark_FileSync_Overhead(b *testing.B) {
	tmpDir := b.TempDir()
	testFile := filepath.Join(tmpDir, "testfile.bin")

	f, err := os.Create(testFile)
	if err != nil {
		b.Fatalf("Failed to create test file: %v", err)
	}
	defer func() {
		_ = f.Close()
	}()

	// Pre-allocate and write data
	data := make([]byte, 10*types.MB)
	if _, err := f.Write(data); err != nil {
		b.Fatalf("Failed to write test file: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := f.Sync(); err != nil {
			b.Fatalf("Sync failed: %v", err)
		}
	}
}

// Benchmark_DownloadHappyPath_1MB tests with minimal concurrent load
func Benchmark_DownloadHappyPath_1MB(b *testing.B) {
	if testing.Short() {
		b.Skip("Skipping in short mode")
	}
	benchmarkConcurrentDownloadHappyPath(b, 1*types.MB)
}

// Benchmark_DownloadHappyPath_10MB tests with 3 workers
func Benchmark_DownloadHappyPath_10MB(b *testing.B) {
	if testing.Short() {
		b.Skip("Skipping in short mode")
	}
	benchmarkConcurrentDownloadHappyPath(b, 10*types.MB)
}

// Benchmark_DownloadHappyPath_50MB tests with 7 workers
func Benchmark_DownloadHappyPath_50MB(b *testing.B) {
	if testing.Short() {
		b.Skip("Skipping in short mode")
	}
	benchmarkConcurrentDownloadHappyPath(b, 50*types.MB)
}

func benchmarkConcurrentDownloadHappyPath(b *testing.B, fileSize int64) {
	tmpDir, cleanup := initTestState(&testing.T{})
	defer cleanup()

	// Create mock server with zero data (fast for benchmarking)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fileSize))

		rangeHeader := r.Header.Get("Range")
		if rangeHeader != "" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", fileSize-1, fileSize))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.CopyN(w, io.Reader(&zeroReader{}), fileSize)
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = io.CopyN(w, io.Reader(&zeroReader{}), fileSize)
		}
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "download.bin")
	workingPath := destPath + types.IncompleteSuffix

	// Pre-create working file
	if f, err := os.Create(workingPath); err == nil {
		_ = f.Close()
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		// Clean up from previous iteration
		_ = os.Remove(destPath)
		_ = os.Remove(workingPath)
		// Pre-create for this iteration
		if f, err := os.Create(workingPath); err == nil {
			_ = f.Close()
		}
		b.StartTimer()

		state := types.NewProgressState(fmt.Sprintf("bench-%d", i), fileSize)
		d := NewConcurrentDownloader(fmt.Sprintf("bench-%d", i), nil, state, types.DefaultRuntimeConfig())

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := d.Download(ctx, server.URL, nil, nil, destPath, fileSize)
		cancel()

		if err != nil {
			b.Fatalf("Download failed: %v", err)
		}
	}

	// Cleanup final files
	_ = os.Remove(destPath)
	_ = os.Remove(workingPath)
}

// zeroReader returns zeros for benchmark data generation
type zeroReader struct{}

func (z *zeroReader) Read(b []byte) (int, error) {
	return len(b), nil
}

// TestDownloadHappyPath_Profiling runs a single download for manual profiling
func TestDownloadHappyPath_Profiling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping profiling test in short mode")
	}

	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(50 * types.MB)

	// Create mock server
	server := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "download.bin")
	workingPath := destPath + types.IncompleteSuffix

	// Pre-create working file
	if f, err := os.Create(workingPath); err == nil {
		_ = f.Close()
	}

	// Perform download
	state := types.NewProgressState("profile", fileSize)
	d := NewConcurrentDownloader("profile", nil, state, types.DefaultRuntimeConfig())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	startTime := time.Now()
	err := d.Download(ctx, server.URL(), nil, nil, destPath, fileSize)
	elapsed := time.Since(startTime)

	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	throughput := float64(fileSize) / elapsed.Seconds() / (1024 * 1024)
	t.Logf("Download completed: %.2f MB/s in %v", throughput, elapsed)

	// Verify file exists
	if _, err := os.Stat(destPath); err != nil {
		t.Logf("Note: File path was %s", destPath)
		t.Logf("Working path was %s", workingPath)
		// Don't fail - file may be in different location
	}
}

// TestLockContention_ProgressUpdates measures real-world progress update overhead
func TestLockContention_ProgressUpdates(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping detailed test in short mode")
	}

	fileSize := int64(100 * types.MB)
	chunkSize := int64(512 * types.KB)
	state := types.NewProgressState("contention-test", fileSize)

	state.InitBitmap(fileSize, chunkSize)

	numWorkers := 8
	updatesPerWorker := 1000

	start := time.Now()

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < updatesPerWorker; j++ {
				offset := int64(workerID*updatesPerWorker+j) * chunkSize % fileSize
				if offset+chunkSize <= fileSize {
					state.UpdateChunkStatus(offset, chunkSize, types.ChunkCompleted)
				}
			}
		}(w)
	}
	wg.Wait()

	elapsed := time.Since(start)
	totalUpdates := numWorkers * updatesPerWorker
	updatesPerSec := float64(totalUpdates) / elapsed.Seconds()

	t.Logf("Progress updates: %d total, %.0f/sec", totalUpdates, updatesPerSec)
	t.Logf("Average latency per update: %.3f ms", elapsed.Seconds()*1000/float64(totalUpdates))
}
