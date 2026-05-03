package secrets

import "github.com/haruotsu/marunage/internal/config"

// auditingStore is a thin decorator that funnels every successful Set /
// Delete through a config.Auditor. The decoration lives outside each
// concrete backend so adding a new backend automatically inherits the
// audit guarantee — there is no way to ship a backend that "forgets"
// to record mutations.
//
// Audit fires AFTER the underlying backend reports success: a failed
// Set does not get an audit line because no mutation happened. Get and
// List pass through untouched so the read hot path stays free of audit
// I/O (mirrors the config.set / config.save scope).
type auditingStore struct {
	inner   Store
	auditor config.Auditor
}

func (a *auditingStore) Backend() string { return a.inner.Backend() }

func (a *auditingStore) Get(name string) (string, bool, error) {
	return a.inner.Get(name)
}

func (a *auditingStore) List() ([]string, error) {
	return a.inner.List()
}

func (a *auditingStore) Set(name, value string) error {
	if err := a.inner.Set(name, value); err != nil {
		return err
	}
	a.auditor.Record(config.AuditEvent{
		Action:  "secrets.set",
		Backend: a.inner.Backend(),
		Name:    name,
		// Value is intentionally omitted — secrets must never appear in
		// audit.log. Backend + name is enough evidence that a mutation
		// happened, and operators can correlate against the source
		// plugin's setup logs if they need more detail.
	})
	return nil
}

func (a *auditingStore) Delete(name string) error {
	if err := a.inner.Delete(name); err != nil {
		return err
	}
	a.auditor.Record(config.AuditEvent{
		Action:  "secrets.delete",
		Backend: a.inner.Backend(),
		Name:    name,
	})
	return nil
}
