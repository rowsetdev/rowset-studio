package rowset

import (
	"net/http"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/activity"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/api"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/id"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/policy"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

// Domain types.
type (
	Identity             = domain.Identity
	Connection           = domain.Connection
	ConnectionNode       = domain.ConnectionNode
	ColumnOrigin         = domain.ColumnOrigin
	User                 = domain.User
	Role                 = domain.Role
	Organization         = domain.Organization
	RoleConnectionAccess = domain.RoleConnectionAccess
	Secret               = domain.Secret
	AuditLog             = domain.AuditLog
	QueryHistory         = domain.QueryHistory
	Decision             = policy.Decision
)

// Extension points of a server; see Server's Add* and Set* methods.
type (
	StatementRequest   = api.StatementRequest
	StatementHook      = api.StatementHook
	ResultRequest      = api.ResultRequest
	ResultTransforms   = api.ResultTransforms
	ResultHook         = api.ResultHook
	Annotations        = api.Annotations
	Escalation         = api.Escalation
	DeferredStatement  = api.DeferredStatement
	DeferredResult     = api.DeferredResult
	RouteRegistrar     = api.RouteRegistrar
	Kit                = api.Kit
	ConnectionSaveHook = api.ConnectionSaveHook
)

// Storage.
type (
	Store         = store.Store
	Storage       = store.Extension
	DefaultPolicy = store.DefaultPolicy
	ActivityStore = activity.Store
)

var (
	ErrNotFound  = store.ErrNotFound
	ErrDuplicate = store.ErrDuplicate
)

// AddStorage adds a plugin's tables and data rules to the control database.
func (s *Setup) AddStorage(storage Storage) { store.RegisterExtension(storage) }

// NewID returns a new random identifier.
func NewID() string { return id.New() }

// Now is the current time as stored in the control database.
func Now() string { return store.NowString() }

// RequestIdentity is the caller of a request that passed Kit.Authenticated or
// Kit.RequireAdmin.
func RequestIdentity(r *http.Request) Identity { return api.Identity(r) }

func WriteJSON(w http.ResponseWriter, status int, value any) { api.WriteJSON(w, status, value) }

func WriteError(w http.ResponseWriter, status int, code, message string) {
	api.WriteError(w, status, code, message)
}

// WriteStoreError maps ErrNotFound and ErrDuplicate to their HTTP responses.
func WriteStoreError(w http.ResponseWriter, err error) { api.WriteStoreError(w, err) }

// DecodeJSON decodes a request body, rejecting unknown fields; on failure it
// has already written the error response.
func DecodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	return api.DecodeJSON(w, r, target)
}
