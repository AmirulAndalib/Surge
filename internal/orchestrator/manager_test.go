package orchestrator

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SurgeDM/Surge/internal/config"
	"github.com/SurgeDM/Surge/internal/scheduler"
	"github.com/SurgeDM/Surge/internal/types"
)

func TestLifecycleManager_Settings(t *testing.T) {
	mgr := NewLifecycleManager(nil, nil)
	defer mgr.Shutdown()

	s := mgr.GetSettings()
	if s == nil {
		t.Fatal("expected default settings, got nil")
	}

	newSettings := config.DefaultSettings()
	newSettings.Network.MaxConcurrentProbes.Value = 10
	mgr.ApplySettings(newSettings)

	s2 := mgr.GetSettings()
	if s2.Network.MaxConcurrentProbes.Value != 10 {
		t.Errorf("expected MaxConcurrentProbes to be 10, got %v", s2.Network.MaxConcurrentProbes.Value)
	}
}

func TestLifecycleManager_EnqueueSuccess(t *testing.T) {
	// Create a test HTTP server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	progressCh := make(chan types.DownloadEvent, 10)
	pool := scheduler.New(progressCh, 1)
	eb := NewEventBus()
	mgr := NewLifecycleManager(pool, eb)
	defer mgr.Shutdown()

	destDir := t.TempDir()

	req := &DownloadRequest{
		URL:      ts.URL + "/testfile.txt",
		Filename: "testfile.txt",
		Path:     destDir,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	id, finalName, err := mgr.Enqueue(ctx, req)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	if id == "" {
		t.Error("expected non-empty ID")
	}
	if finalName != "testfile.txt" {
		t.Errorf("expected testfile.txt, got %s", finalName)
	}

	// Verify working file was created
	surgePath := filepath.Join(destDir, finalName) + types.IncompleteSuffix
	if _, err := os.Stat(surgePath); os.IsNotExist(err) {
		t.Errorf("expected working file to be created at %s", surgePath)
	}

	// Verify DownloadQueuedMsg was published
	sub, cleanup := eb.Subscribe()
	defer cleanup()

	// Wait a moment for async event to be broadcasted if any, though Enqueue synchronously calls eb.Publish
	// We need to check if the event reached the subscriber.
	found := false
	timeout := time.After(500 * time.Millisecond)
	for !found {
		select {
		case <-sub:
			found = true
		case <-timeout:
			t.Fatal("timed out waiting for DownloadQueuedMsg")
		}
	}
}

func TestLifecycleManager_EnqueueWithID(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()

	progressCh := make(chan types.DownloadEvent, 10)
	pool := scheduler.New(progressCh, 1)
	eb := NewEventBus()
	mgr := NewLifecycleManager(pool, eb)
	defer mgr.Shutdown()

	destDir := t.TempDir()

	req := &DownloadRequest{
		URL:      ts.URL + "/test.zip",
		Filename: "test.zip",
		Path:     destDir,
	}

	customID := "my-custom-uuid-1234"
	id, _, err := mgr.EnqueueWithID(context.Background(), req, customID)
	if err != nil {
		t.Fatalf("EnqueueWithID failed: %v", err)
	}

	if id != customID {
		t.Errorf("expected custom ID %s, got %s", customID, id)
	}
}

func TestLifecycleManager_IsNameActive(t *testing.T) {
	activeFunc := func(dir, name string) bool {
		return name == "active.txt"
	}

	mgr := NewLifecycleManager(nil, nil, activeFunc)

	if !mgr.IsNameActive("/tmp", "active.txt") {
		t.Error("expected true for active.txt")
	}
	if mgr.IsNameActive("/tmp", "other.txt") {
		t.Error("expected false for other.txt")
	}
}

func TestLifecycleManager_EnqueueInvalid(t *testing.T) {
	mgr := NewLifecycleManager(nil, nil)

	// Missing Pool
	_, _, err := mgr.Enqueue(context.Background(), &DownloadRequest{URL: "http://example.com", Path: "/tmp"})
	if !errors.Is(err, types.ErrServiceUnavailable) {
		t.Errorf("expected ErrServiceUnavailable, got %v", err)
	}

	pool := scheduler.New(make(chan types.DownloadEvent, 1), 1)
	mgr = NewLifecycleManager(pool, nil)

	// Missing URL
	_, _, err = mgr.Enqueue(context.Background(), &DownloadRequest{Path: "/tmp"})
	if !errors.Is(err, types.ErrURLRequired) {
		t.Errorf("expected ErrURLRequired, got %v", err)
	}

	// Missing Path
	_, _, err = mgr.Enqueue(context.Background(), &DownloadRequest{URL: "http://example.com"})
	if !errors.Is(err, types.ErrDestRequired) {
		t.Errorf("expected ErrDestRequired, got %v", err)
	}
}
