package api

import (
	"context"
	"time"
)

// auditVerifyInterval is how often the audit hash chain is walked end to
// end. The chain only grows by appending, so a break introduced by editing
// the database directly stays detectable at the next pass; this doesn't need
// to run often. The first pass waits so it never competes with start-up.
const auditVerifyInterval = 24 * time.Hour

var auditVerifyDelay = 2 * time.Minute

// auditIntegrityCheck periodically confirms every organization's audit hash
// chain still matches what was written, the only thing standing between
// "tamper-evident" and "tamper-evident in theory": PrepareAudit chains each
// entry to the one before it, but nothing walked that chain back before this
// existed (see VerifyAuditChain).
func (s *Server) auditIntegrityCheck(ctx context.Context) {
	timer := time.NewTimer(auditVerifyDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		breaks, checked, err := s.activity.VerifyAuditChain(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Error("audit chain verification failed to run", "error", err)
			}
		} else if len(breaks) > 0 {
			for _, chainBreak := range breaks {
				s.logger.Error("audit hash chain is broken; an entry no longer matches what it was written with", "org_id", chainBreak.OrgID, "audit_id", chainBreak.AuditID)
			}
		} else {
			s.logger.Debug("audit chain verification passed", "entries_checked", checked)
		}
		timer.Reset(auditVerifyInterval)
	}
}
