package service

import "go.klarlabs.de/warden/internal/domain"

// Audit produces a provenance report of every commit since adoption, for
// compliance export (SOC2-style "prove each commit was reviewed + tested").
// It is the same walk as Doctor — reading refs/notes/warden and classifying via
// the domain model — but named for its reporting intent: doctor is a health
// gate (exits non-zero on drift), audit is an informational export. The
// CommitStatus values already carry run ID, steps, and chain status, which is
// all the export needs, so this is a thin semantic alias rather than a second
// walk.
func (s *Service) Audit(branch string) (domain.AuditReport, error) {
	return s.Doctor(branch)
}
