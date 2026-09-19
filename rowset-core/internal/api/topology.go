package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

var (
	errSecondaryUnsafe = errors.New("secondary nodes accept read-only SELECT statements only")
	errSecondaryTxn    = errors.New("interactive transactions are disabled on secondary nodes")
)

func effectiveNodeRole(policy, defaultRole string, requested *string) (string, error) {
	switch policy {
	case "primary_only":
		return "primary", nil
	case "secondary_only":
		return "secondary", nil
	case "user_selectable":
		if defaultRole != "primary" && defaultRole != "secondary" {
			return "", errors.New("invalid node routing policy")
		}
		if requested != nil {
			if *requested != "primary" && *requested != "secondary" {
				return "", errors.New("invalid node routing policy")
			}
			return *requested, nil
		}
		return defaultRole, nil
	default:
		return "", errors.New("invalid node routing policy")
	}
}

func (s *Server) desiredNodeRole(ctx context.Context, identity domain.Identity, connectionID string, requested *string) (string, error) {
	if identity.IsAdmin() {
		return effectiveNodeRole("user_selectable", "primary", requested)
	}
	role, err := s.store.UserRole(ctx, identity.UserID)
	if err != nil {
		return "", errors.New("role has no access to this connection")
	}
	access, err := s.store.ListRoleConnectionAccess(ctx, role.ID)
	if err != nil {
		return "", err
	}
	for _, item := range access {
		if item.ConnectionID == connectionID {
			return effectiveNodeRole(item.NodePolicy, item.DefaultNodeRole, requested)
		}
	}
	return "", errors.New("role has no access to this connection")
}

func (s *Server) routedEngineConnection(ctx context.Context, identity domain.Identity, connection domain.Connection, database string, requested *string, info *sqlguard.Info) (engine.Connection, string, error) {
	desired, err := s.desiredNodeRole(ctx, identity, connection.ID, requested)
	if err != nil {
		return engine.Connection{}, "", err
	}
	if desired == "secondary" && (info == nil || !sqlguard.SecondarySafe(*info)) {
		return engine.Connection{}, desired, errSecondaryUnsafe
	}
	node, err := s.routeNode(ctx, connection, desired)
	if err != nil {
		return engine.Connection{}, desired, err
	}
	target, err := s.engineConnectionAt(ctx, connection, database, node.Host, node.Port)
	return target, desired, err
}

func (s *Server) routeNode(ctx context.Context, connection domain.Connection, desired string) (domain.ConnectionNode, error) {
	nodes, err := s.store.ListConnectionNodes(ctx, connection.ID)
	if err != nil {
		return domain.ConnectionNode{}, err
	}
	if node, ok := s.freshNode(nodes, desired); ok {
		return node, nil
	}
	s.refreshConnectionTopology(ctx, connection.ID)
	nodes, err = s.store.ListConnectionNodes(ctx, connection.ID)
	if err != nil {
		return domain.ConnectionNode{}, err
	}
	if node, ok := s.freshNode(nodes, desired); ok {
		return node, nil
	}
	return domain.ConnectionNode{}, fmt.Errorf("no healthy verified %s node is available", desired)
}

func (s *Server) metadataEngineConnection(ctx context.Context, connection domain.Connection, database string) (engine.Connection, error) {
	nodes, err := s.store.ListConnectionNodes(ctx, connection.ID)
	if err != nil {
		return engine.Connection{}, err
	}
	choose := func(items []domain.ConnectionNode) (domain.ConnectionNode, bool) {
		if node, ok := s.freshNode(items, "primary"); ok {
			return node, true
		}
		if node, ok := s.freshNode(items, "secondary"); ok {
			return node, true
		}
		return domain.ConnectionNode{}, false
	}
	node, ok := choose(nodes)
	if !ok {
		s.refreshConnectionTopology(ctx, connection.ID)
		nodes, err = s.store.ListConnectionNodes(ctx, connection.ID)
		if err != nil {
			return engine.Connection{}, err
		}
		node, ok = choose(nodes)
	}
	if !ok {
		return engine.Connection{}, errors.New("no healthy verified node is available")
	}
	return s.engineConnectionAt(ctx, connection, database, node.Host, node.Port)
}

func (s *Server) freshNode(nodes []domain.ConnectionNode, desired string) (domain.ConnectionNode, bool) {
	maxAge := time.Duration(s.topologyIntervalSeconds()*3) * time.Second
	if maxAge < 30*time.Second {
		maxAge = 30 * time.Second
	}
	for _, node := range nodes {
		if node.Health == "healthy" && node.DetectedRole == desired && (desired != "secondary" || node.ReadOnly) && nodeFresh(node, maxAge) {
			return node, true
		}
	}
	return domain.ConnectionNode{}, false
}

func nodeFresh(node domain.ConnectionNode, maxAge time.Duration) bool {
	if node.LastCheckedAt == nil {
		return false
	}
	checked, err := time.Parse(time.RFC3339Nano, *node.LastCheckedAt)
	if err != nil {
		return false
	}
	age := time.Since(checked)
	return age >= 0 && age <= maxAge
}

func (s *Server) connectionTopologyLock(connectionID string) *sync.Mutex {
	s.topologyMu.Lock()
	defer s.topologyMu.Unlock()
	lock := s.topologyLocks[connectionID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.topologyLocks[connectionID] = lock
	}
	return lock
}

func (s *Server) refreshConnectionTopology(ctx context.Context, connectionID string) {
	lock := s.connectionTopologyLock(connectionID)
	lock.Lock()
	defer lock.Unlock()
	connection, err := s.store.Connection(ctx, connectionID)
	if err != nil {
		return
	}
	nodes, err := s.store.ListConnectionNodes(ctx, connectionID)
	if err != nil {
		return
	}
	var wait sync.WaitGroup
	limit := make(chan struct{}, 4)
	for _, item := range nodes {
		node := item
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case limit <- struct{}{}:
				defer func() { <-limit }()
			case <-ctx.Done():
				return
			}
			probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			role, readOnly, probeErr := s.probeTopologyNode(probeCtx, connection, node)
			if probeErr != nil {
				message := "health check failed"
				_ = s.store.UpdateConnectionNodeStatus(context.WithoutCancel(ctx), node.ID, "unknown", "unreachable", true, &message)
				s.logger.Warn("topology health check failed", "connection_id", connection.ID, "node", node.Name, "error", probeErr)
				return
			}
			_ = s.store.UpdateConnectionNodeStatus(context.WithoutCancel(ctx), node.ID, role, "healthy", readOnly, nil)
		}()
	}
	wait.Wait()
}

func (s *Server) probeTopologyNode(ctx context.Context, connection domain.Connection, node domain.ConnectionNode) (string, bool, error) {
	target, err := s.engineConnectionAt(ctx, connection, "", node.Host, node.Port)
	if err != nil {
		return "", false, err
	}
	if engine.AdditionalEngine(connection.Engine) {
		return "primary", false, s.engines.Test(ctx, target)
	}
	query := ""
	switch strings.ToLower(connection.Engine) {
	case "postgres", "postgresql", "cockroachdb":
		query = "SELECT CASE WHEN pg_is_in_recovery() THEN 'secondary' ELSE 'primary' END, current_setting('transaction_read_only')"
	case "mysql":
		query = "SELECT IF(@@global.read_only = 1 AND @@global.super_read_only = 1, 'secondary', 'primary'), IF(@@global.read_only = 1 AND @@global.super_read_only = 1, 1, 0)"
	case "mariadb":
		query = "SELECT IF(@@global.read_only = 1, 'secondary', 'primary'), @@global.read_only"
	case "mssql", "sqlserver":
		// master is not an availability database, so fn_hadr_is_primary_replica
		// returns NULL there. Fall back to the local replica state when this
		// instance hosts only one role; that keeps a connection configured with
		// master able to distinguish the two servers in a normal two-node AG.
		query = "SELECT CASE WHEN sys.fn_hadr_is_primary_replica(DB_NAME()) = 0 THEN 'secondary' WHEN sys.fn_hadr_is_primary_replica(DB_NAME()) = 1 THEN 'primary' WHEN EXISTS (SELECT 1 FROM sys.dm_hadr_availability_replica_states WHERE is_local = 1 AND role = 2) AND NOT EXISTS (SELECT 1 FROM sys.dm_hadr_availability_replica_states WHERE is_local = 1 AND role = 1) THEN 'secondary' ELSE 'primary' END, CASE WHEN DATABASEPROPERTYEX(DB_NAME(), 'Updateability') = 'READ_ONLY' THEN 1 WHEN EXISTS (SELECT 1 FROM sys.dm_hadr_availability_replica_states WHERE is_local = 1 AND role = 2) AND NOT EXISTS (SELECT 1 FROM sys.dm_hadr_availability_replica_states WHERE is_local = 1 AND role = 1) THEN 1 ELSE 0 END"
	default:
		return "", false, errors.New("unsupported engine")
	}
	result, err := s.engines.Execute(ctx, target, query, 2)
	if err != nil {
		return "", false, err
	}
	if len(result.Rows) == 0 || len(result.Rows[0]) < 2 {
		return "", false, errors.New("topology check returned no row")
	}
	role := strings.ToLower(strings.TrimSpace(fmt.Sprint(result.Rows[0][0])))
	if role != "primary" && role != "secondary" {
		return "", false, errors.New("database returned an unknown topology role")
	}
	readOnly := topologyTruthy(result.Rows[0][1])
	if role == "secondary" && !readOnly {
		return "", false, errors.New("secondary node is not read-only")
	}
	return role, readOnly || role == "secondary", nil
}

func topologyTruthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case int64:
		return typed != 0
	case int32:
		return typed != 0
	case int:
		return typed != 0
	case string:
		value := strings.ToLower(strings.TrimSpace(typed))
		return value == "1" || value == "true" || value == "on"
	default:
		return false
	}
}

func (s *Server) topologyIntervalSeconds() uint64 {
	if s.config.TopologyCheckIntervalSecs == 0 {
		return 30
	}
	return s.config.TopologyCheckIntervalSecs
}

func (s *Server) topologyRefresher(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(s.topologyIntervalSeconds()) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshAllTopologies(ctx)
		}
	}
}

func (s *Server) refreshAllTopologies(ctx context.Context) {
	nodes, err := s.store.ListAllConnectionNodes(ctx)
	if err != nil {
		return
	}
	ids := make(map[string]struct{})
	for _, node := range nodes {
		ids[node.ConnectionID] = struct{}{}
	}
	for connectionID := range ids {
		if ctx.Err() != nil {
			return
		}
		s.refreshConnectionTopology(ctx, connectionID)
	}
}
