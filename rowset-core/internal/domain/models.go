package domain

import "strings"

// Identity is the authenticated user context carried by access tokens and
// passed through policy, query and audit layers.
type Identity struct {
	UserID string `json:"user_id"`
	OrgID  string `json:"org_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

// ColumnOrigin is provenance reported by the database before result rows.
// Resolved is true only when Schema/Table/Column identify a physical source;
// Expression marks computed output for which the wire metadata has no source.
type ColumnOrigin struct {
	Schema     string
	Table      string
	Column     string
	Resolved   bool
	Expression bool
}

func (i Identity) IsAdmin() bool { return equalFoldASCII(i.Role, "admin") }

type User struct {
	ID           string `json:"id"`
	OrgID        string `json:"orgId"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	Status       string `json:"status"`
	CreatedAt    string `json:"createdAt"`
}

type Organization struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
}

type Role struct {
	ID         string `json:"id"`
	OrgID      string `json:"orgId"`
	Name       string `json:"name"`
	IsReadOnly bool   `json:"isReadonly"`
}

type RoleConnectionAccess struct {
	ConnectionID    string `json:"connectionId"`
	AccessLevel     string `json:"accessLevel"`
	NodePolicy      string `json:"nodePolicy"`
	DefaultNodeRole string `json:"defaultNodeRole"`
}

type Connection struct {
	ID                  string  `json:"id"`
	OrgID               string  `json:"orgId"`
	Name                string  `json:"name"`
	Alias               *string `json:"alias,omitempty"`
	Engine              string  `json:"engine"`
	Host                string  `json:"host"`
	Port                int     `json:"port"`
	Database            string  `json:"database"`
	Environment         string  `json:"environment"`
	TLSRequired         bool    `json:"tlsRequired"`
	TLSMode             string  `json:"tlsMode"`
	TLSServerName       string  `json:"tlsServerName"`
	TLSCAPEM            string  `json:"tlsCaPem"`
	TLSClientCertPEM    string  `json:"tlsClientCertPem"`
	TLSClientKeySecret  string  `json:"-"`
	ConnectionUsername  string  `json:"connectionUsername"`
	SecretID            string  `json:"-"`
	CreatedAt           string  `json:"createdAt"`
	QueryTimeoutSeconds int64   `json:"queryTimeoutSeconds"`
	// ReadOnly blocks every write statement on this connection regardless of
	// role, the same guardrail role.IsReadOnly already enforces for a role.
	ReadOnly             bool   `json:"readOnly"`
	CassandraConsistency string `json:"cassandraConsistency"`
	CassandraPageSize    int    `json:"cassandraPageSize"`
	// SSH tunnel. SSHHost empty means the database is reached directly.
	SSHHost               string `json:"sshHost"`
	SSHPort               int    `json:"sshPort"`
	SSHUser               string `json:"sshUser"`
	SSHAuthMethod         string `json:"sshAuthMethod"`
	SSHKnownHost          string `json:"sshKnownHost"`
	SSHSecretID           string `json:"-"`
	SSHPassphraseSecretID string `json:"-"`
}

// LegacyTLSMode reproduces the verification each engine applied when only an
// on/off flag existed: PostgreSQL and SQL Server skipped certificate checks.
func LegacyTLSMode(engine string, required bool) string {
	switch {
	case !required:
		return "disable"
	case strings.EqualFold(engine, "mysql") || strings.EqualFold(engine, "mariadb"):
		return "verify-full"
	default:
		return "require"
	}
}

// EffectiveTLSMode falls back to the legacy flag for records without a mode.
func (c Connection) EffectiveTLSMode() string {
	if c.TLSMode != "" {
		return c.TLSMode
	}
	return LegacyTLSMode(c.Engine, c.TLSRequired)
}

type ConnectionNode struct {
	ID            string  `json:"id"`
	ConnectionID  string  `json:"connectionId"`
	Name          string  `json:"name"`
	Host          string  `json:"host"`
	Port          int     `json:"port"`
	DetectedRole  string  `json:"detectedRole"`
	Health        string  `json:"health"`
	ReadOnly      bool    `json:"readOnly"`
	LastCheckedAt *string `json:"lastCheckedAt,omitempty"`
	LastError     *string `json:"lastError,omitempty"`
	CreatedAt     string  `json:"createdAt"`
}

type Secret struct {
	ID         string
	Ciphertext []byte
	Nonce      []byte
}

type PolicyOverride struct {
	Key     string  `json:"key"`
	Enabled bool    `json:"enabled"`
	Config  *string `json:"config,omitempty"`
}

type CustomPolicy struct {
	ID           string  `json:"id"`
	OrgID        string  `json:"orgId"`
	Name         string  `json:"name"`
	Kind         string  `json:"kind"`
	Config       string  `json:"config"`
	ConnectionID *string `json:"connectionId,omitempty"`
	RoleName     *string `json:"roleName,omitempty"`
	Enabled      bool    `json:"enabled"`
	CreatedAt    string  `json:"createdAt"`
}

type SavedQuery struct {
	ID           string `json:"id"`
	OrgID        string `json:"orgId"`
	UserID       string `json:"userId"`
	ConnectionID string `json:"connectionId"`
	Name         string `json:"name"`
	SQL          string `json:"sql"`
	CreatedAt    string `json:"createdAt"`
}

type QueryHistory struct {
	ID            string `json:"id"`
	UserID        string `json:"-"`
	ConnectionID  string `json:"-"`
	SQL           string `json:"sql"`
	NormalizedSQL string `json:"-"`
	QueryHash     string `json:"-"`
	Status        string `json:"status"`
	RowsReturned  int64  `json:"rowsReturned"`
	DurationMS    int64  `json:"durationMs"`
	CreatedAt     string `json:"createdAt"`
}

type AuditLog struct {
	ID, UserID, OrgID, Role, ConnectionID                           string
	SQL, NormalizedSQL, QueryHash                                   string
	ClientType, ClientIP, UserAgent                                 string
	StartedAt                                                       string
	DurationMS, RowsReturned, RowsAffected                          int64
	RowsReturnedSet, RowsAffectedSet                                bool
	PolicyDecision, PolicyReason, PolicyID, Reference, ErrorMessage string
	CreatedAt, PreviousHash, EntryHash                              string
	HashVersion                                                     int64
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		ac, bc := a[i], b[i]
		if ac >= 'A' && ac <= 'Z' {
			ac += 'a' - 'A'
		}
		if bc >= 'A' && bc <= 'Z' {
			bc += 'a' - 'A'
		}
		if ac != bc {
			return false
		}
	}
	return true
}
