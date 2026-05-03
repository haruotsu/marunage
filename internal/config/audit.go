package config

// AuditEvent describes a single mutation worth recording in audit.log.
// Every change site funnels through this struct so audit coverage is
// guaranteed for the documented invariant "No silent execution"; the
// file-backed implementation lives in internal/logging.AuditLog.
//
// Field set is intentionally a flat union of "what does config want to
// log" and "what does secrets want to log", because audit.log readers
// (operators, security review) need a single grep-able shape. Empty
// fields serialise as JSON omitempty and disappear from the on-disk
// record so a config.set entry does not carry an empty backend="" key.
type AuditEvent struct {
	Action  string // e.g. "config.save", "config.set", "secrets.set", "secrets.delete"
	Path    string // absolute path of the file mutated (config saves)
	Key     string // dotted-path key for "config.set"; empty for whole-file saves
	Value   string // stringified new value, when applicable. NEVER set for secrets.*
	Backend string // secrets backend identifier (file/keyring/env/...) for secrets.* events
	Name    string // secret name for secrets.* events
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
