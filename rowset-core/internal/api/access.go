package api

import (
	"context"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
)

// AccessRole is what a user may do in general: Name "admin" manages the
// workspace and uses every connection; ReadOnly allows reads only. ID lets
// hooks scope their own rules by role.
type AccessRole struct {
	ID, Name string
	ReadOnly bool
}

// ConnectionGrant is a user's access to one connection. NodePolicy and
// DefaultNodeRole choose between a primary and its readable secondaries.
type ConnectionGrant struct {
	NodePolicy, DefaultNodeRole string
}

// Access decides what users other than the workspace owner may do. Without
// one, every user is an administrator, which is right for a workspace whose
// only user is its owner.
type Access interface {
	// Role returns the user's role.
	Role(ctx context.Context, userID string) (AccessRole, error)
	// Connection returns the grant of a non-admin user on a connection of
	// their organization; ok is false when they may not use it.
	Connection(ctx context.Context, identity domain.Identity, role AccessRole, connectionID string) (grant ConnectionGrant, ok bool, err error)
}

// SetAccess installs the access rules; call before Handler.
func (s *Server) SetAccess(access Access) { s.access = access }

type ownerAccess struct{}

func (ownerAccess) Role(context.Context, string) (AccessRole, error) {
	return AccessRole{Name: "admin"}, nil
}

func (ownerAccess) Connection(context.Context, domain.Identity, AccessRole, string) (ConnectionGrant, bool, error) {
	return ConnectionGrant{}, false, nil
}

func (s *Server) accessRules() Access {
	if s.access == nil {
		return ownerAccess{}
	}
	return s.access
}

// role returns a user's role under the installed access rules.
func (s *Server) role(ctx context.Context, userID string) (AccessRole, error) {
	return s.accessRules().Role(ctx, userID)
}

// connectionGrant returns the caller's grant on a connection of their
// organization. Administrators use every connection and choose the node.
func (s *Server) connectionGrant(ctx context.Context, identity domain.Identity, connectionID string) (ConnectionGrant, bool) {
	if identity.IsAdmin() {
		return ConnectionGrant{NodePolicy: "user_selectable", DefaultNodeRole: "primary"}, true
	}
	role, err := s.role(ctx, identity.UserID)
	if err != nil {
		return ConnectionGrant{}, false
	}
	grant, ok, err := s.accessRules().Connection(ctx, identity, role, connectionID)
	return grant, ok && err == nil
}

// canUseConnection reports whether the caller may use a connection.
func (s *Server) canUseConnection(ctx context.Context, identity domain.Identity, connection domain.Connection) bool {
	if connection.OrgID != identity.OrgID {
		return false
	}
	_, ok := s.connectionGrant(ctx, identity, connection.ID)
	return ok
}
