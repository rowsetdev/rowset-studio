package api

import (
	"context"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/id"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/policy"
)

// recordClientActivity writes history and an activity entry for a statement
// that did not arrive through an HTTP query request.
func (s *Server) recordClientActivity(ctx context.Context, identity domain.Identity, connection domain.Connection, sql, normalized, hash, clientType, clientIP, userAgent string, rows, duration int64, command bool, status string, decision policy.Decision, decisionName, reference, errorMessage string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_ = s.activity.CreateQueryHistory(ctx, domain.QueryHistory{ID: id.New(), UserID: identity.UserID, ConnectionID: connection.ID, SQL: sql, NormalizedSQL: normalized, QueryHash: hash, Status: status, RowsReturned: rows, DurationMS: duration, CreatedAt: now})
	entry := domain.AuditLog{ID: id.New(), UserID: identity.UserID, OrgID: identity.OrgID, Role: identity.Role, ConnectionID: connection.ID, SQL: sql, NormalizedSQL: normalized, QueryHash: hash, ClientType: clientType, ClientIP: clientIP, UserAgent: userAgent, StartedAt: now, DurationMS: duration, PolicyDecision: decisionName, PolicyReason: decision.Reason, PolicyID: decision.PolicyID, Reference: reference, ErrorMessage: errorMessage, CreatedAt: now}
	if command {
		entry.RowsAffected = rows
		entry.RowsAffectedSet = true
	} else {
		entry.RowsReturned = rows
		entry.RowsReturnedSet = true
	}
	_ = s.activity.WriteAudit(ctx, entry)
}
