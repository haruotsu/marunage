package secrets_test

import (
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/secrets"
)

// recordingAuditor mirrors the helper in internal/config/loader_test.go so
// tests can assert exactly which audit events were emitted, in order.
type recordingAuditor struct {
	events []config.AuditEvent
}

func (r *recordingAuditor) Record(e config.AuditEvent) {
	r.events = append(r.events, e)
}

// TestAuditEmitsOnSetAndDeleteWithoutValue is the security-critical test
// for PR-30: every Set / Delete must produce one audit line that names
// the action, the backend, and the secret, but NEVER the value. A
// regression here would let tokens leak into ~/.marunage/logs/audit.log
// and defeat the whole point of having a secret store.
func TestAuditEmitsOnSetAndDeleteWithoutValue(t *testing.T) {
	rec := &recordingAuditor{}
	store, err := secrets.OpenWithAuditor(
		secrets.Config{Backend: "file", HomeDir: t.TempDir()},
		rec,
	)
	if err != nil {
		t.Fatalf("OpenWithAuditor: %v", err)
	}

	const veryPrivate = "ya29.super-secret-token-do-not-log"

	if err := store.Set("gmail", veryPrivate); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Delete("gmail"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if len(rec.events) != 2 {
		t.Fatalf("events = %d; want 2 (one per Set/Delete)", len(rec.events))
	}

	set := rec.events[0]
	if set.Action != "secrets.set" {
		t.Errorf("Set event action = %q; want %q", set.Action, "secrets.set")
	}
	if set.Backend != "file" {
		t.Errorf("Set event backend = %q; want %q", set.Backend, "file")
	}
	if set.Name != "gmail" {
		t.Errorf("Set event name = %q; want %q", set.Name, "gmail")
	}

	del := rec.events[1]
	if del.Action != "secrets.delete" {
		t.Errorf("Delete event action = %q; want %q", del.Action, "secrets.delete")
	}
	if del.Backend != "file" {
		t.Errorf("Delete event backend = %q; want %q", del.Backend, "file")
	}
	if del.Name != "gmail" {
		t.Errorf("Delete event name = %q; want %q", del.Name, "gmail")
	}

	for i, e := range rec.events {
		if strings.Contains(e.Value, veryPrivate) {
			t.Errorf("event[%d].Value contains the secret value; audit must never log values", i)
		}
		// Belt and braces: also check Key, Path, etc.
		if strings.Contains(e.Key, veryPrivate) || strings.Contains(e.Path, veryPrivate) {
			t.Errorf("event[%d] leaks the secret value into another field: %+v", i, e)
		}
	}
}

// TestAuditNotEmittedOnGet pins the noise floor: Get is the hot path for
// every dispatch, so it must not generate an audit line. Only mutations
// (Set / Delete) are auditable; reads belong to the daemon log if at
// all, never to audit.log.
func TestAuditNotEmittedOnGet(t *testing.T) {
	rec := &recordingAuditor{}
	store, err := secrets.OpenWithAuditor(
		secrets.Config{Backend: "file", HomeDir: t.TempDir()},
		rec,
	)
	if err != nil {
		t.Fatalf("OpenWithAuditor: %v", err)
	}
	if err := store.Set("gmail", "tok"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// One audit line so far (the Set above). Get / List must add zero.
	baseline := len(rec.events)

	if _, _, err := store.Get("gmail"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := store.List(); err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(rec.events) != baseline {
		t.Errorf("audit grew from %d to %d on Get/List; reads must not audit", baseline, len(rec.events))
	}
}

// TestAuditNotEmittedWhenSetFails closes a subtle gap: if a backend
// rejects a write (e.g. invalid name -> validateName error), no audit
// line should fire because no mutation actually happened. Otherwise
// audit.log diverges from on-disk reality, which defeats its purpose
// as evidence.
func TestAuditNotEmittedWhenSetFails(t *testing.T) {
	rec := &recordingAuditor{}
	store, err := secrets.OpenWithAuditor(
		secrets.Config{Backend: "file", HomeDir: t.TempDir()},
		rec,
	)
	if err != nil {
		t.Fatalf("OpenWithAuditor: %v", err)
	}

	// "../escape" is rejected by validateName before any disk I/O.
	if err := store.Set("../escape", "tok"); err == nil {
		t.Fatal("Set with bad name = nil; want error")
	}
	if len(rec.events) != 0 {
		t.Errorf("audit fired for failed Set: %+v", rec.events)
	}
}
