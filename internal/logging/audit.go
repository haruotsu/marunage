// Package logging owns marunage's structured log writers. The package
// provides two concrete sinks that the rest of the binary funnels through:
//
//   - AuditLog: append-only ~/.marunage/logs/audit.log used to satisfy the
//     "No silent execution" invariant from docs/requirement.md. Every config
//     mutation, dispatch, and reflection invocation is required to leave a
//     trace here.
//   - Logger / RotatingFile: JSON Lines daemon log written to
//     ~/.marunage/logs/daemon.log with size-based rotation so the file does
//     not grow unbounded under long-running `marunage daemon`.
//
// The package depends on internal/config for the AuditEvent shape so the
// audit interface defined in PR-03 has a single shared vocabulary.
package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/haruotsu/marunage/internal/config"
)

// AuditLog appends one JSON Lines record per config mutation. The struct is
// safe for concurrent Record calls; downstream packages share a single
// instance per process.
type AuditLog struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

// NewAuditLog opens or creates path in append-only mode. The parent
// directory is created with 0700 and the file with 0600 because audit
// entries can carry sensitive config values (tokens are filtered upstream,
// but defense-in-depth is cheap here).
func NewAuditLog(path string) (*AuditLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &AuditLog{path: path, f: f}, nil
}

// Close releases the underlying file. Subsequent Record calls become quiet
// no-ops so a deferred Close cannot crash a stray writer goroutine.
func (a *AuditLog) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		return nil
	}
	err := a.f.Close()
	a.f = nil
	return err
}

// Record appends one JSON line for e. Errors are swallowed deliberately:
// an unwritable audit log must not crash the caller mid-dispatch, and the
// process-level health check covers the disk-full / permission cases via a
// separate path.
func (a *AuditLog) Record(e config.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		return
	}

	record := struct {
		Time   string `json:"time"`
		Action string `json:"action"`
		Path   string `json:"path,omitempty"`
		Key    string `json:"key,omitempty"`
		Value  string `json:"value,omitempty"`
	}{
		Time:   time.Now().UTC().Format(time.RFC3339Nano),
		Action: e.Action,
		Path:   e.Path,
		Key:    e.Key,
		Value:  e.Value,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	data = append(data, '\n')
	_, _ = a.f.Write(data)
}
