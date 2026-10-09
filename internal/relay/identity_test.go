package relay

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newIdentityRunner(path string) *Runner {
	return &Runner{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg: Config{IdentityPath: path},
	}
}

func testIdentity(secret string) Identity {
	return Identity{RelayID: "r1", RelaySecret: secret, ClaimedAt: time.Now().UTC(), PlatformURL: "http://localhost:8080", ConfigPollIntervalSeconds: 30}
}

// A relay that crashes or is killed while rewriting identity.json must not leave a
// truncated file behind: an unreadable identity makes it re-register as a new relay.
// A reader racing the writer must therefore only ever see a complete identity.
func TestSaveIdentity_ReaderNeverSeesPartialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	r := newIdentityRunner(path)
	big := testIdentity(strings.Repeat("s", 64<<10)) // large enough that a write is not a single tiny syscall
	if err := r.saveIdentity(big); err != nil {
		t.Fatalf("initial save: %v", err)
	}

	var stop atomic.Bool
	var bad atomic.Value
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			b, err := os.ReadFile(path)
			if err != nil {
				bad.Store("read error: " + err.Error())
				return
			}
			var id Identity
			if err := json.Unmarshal(b, &id); err != nil || id.RelaySecret == "" {
				bad.Store("saw partial/empty identity file (" + strconv.Itoa(len(b)) + " bytes)")
				return
			}
		}
	}()

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) && bad.Load() == nil {
		if err := r.saveIdentity(big); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	stop.Store(true)
	wg.Wait()
	if msg := bad.Load(); msg != nil {
		t.Fatal(msg)
	}
}

func TestSaveIdentity_LeavesNoTempFilesAndKeepsMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")
	r := newIdentityRunner(path)
	for i := 0; i < 3; i++ {
		if err := r.saveIdentity(testIdentity("sec")); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "identity.json" {
		t.Fatalf("expected only identity.json in %s, got %v", dir, entries)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("identity file mode = %v, want 0600", info.Mode().Perm())
	}
}

// Replacing a file via rename fails when the file is a single-file bind mount
// (EBUSY). The relay must still persist its identity in that case.
func TestSaveIdentity_FallsBackToDirectWriteWhenRenameFails(t *testing.T) {
	orig := renameFile
	renameFile = func(_, _ string) error { return errors.New("device or resource busy") }
	defer func() { renameFile = orig }()

	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")
	r := newIdentityRunner(path)
	if err := r.saveIdentity(testIdentity("sec")); err != nil {
		t.Fatalf("save should fall back to a direct write, got %v", err)
	}
	loaded, err := r.loadIdentity()
	if err != nil || loaded.RelaySecret != "sec" {
		t.Fatalf("identity not persisted via fallback: %+v err=%v", loaded, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temp file left behind after failed rename: %v", entries)
	}
}
