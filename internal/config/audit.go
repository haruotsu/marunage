package config

// AuditEvent describes a single configuration mutation. Every change site
// funnels through this struct so audit coverage is guaranteed for the
// documented invariant "No silent execution"; the file-backed implementation
// lives in internal/logging.AuditLog.
type AuditEvent struct {
	Action string // e.g. "config.save", "config.set"
	Path   string // absolute path of the config file mutated
	Key    string // dotted-path key for "config.set"; empty for whole-file saves
	Value  string // stringified new value, when applicable
}

// Auditor receives configuration mutation events. Production callers pass a
// *logging.AuditLog; tests and one-shot tools that genuinely need no audit
// trail can pass NopAuditor.
type Auditor interface {
	Record(AuditEvent)
}

// NopAuditor is a sentinel for callers that do not yet need audit logging.
type NopAuditor struct{}

func (NopAuditor) Record(AuditEvent) {}
