package config

// AuditEvent describes a single configuration mutation. PR-04 will replace
// the in-memory Auditor with one that appends to ~/.marunage/logs/audit.log;
// every change site here funnels through this interface so audit coverage is
// guaranteed for the documented invariant "No silent execution".
type AuditEvent struct {
	Action string // e.g. "config.save", "config.set"
	Path   string // absolute path of the config file mutated
	Key    string // dotted-path key for "config.set"; empty for whole-file saves
	Value  string // stringified new value, when applicable
}

// Auditor receives configuration mutation events. PR-03 only ships the
// interface and a no-op default; PR-04 wires the structured logger.
type Auditor interface {
	Record(AuditEvent)
}

// NopAuditor is a sentinel for callers that do not yet need audit logging.
type NopAuditor struct{}

func (NopAuditor) Record(AuditEvent) {}
