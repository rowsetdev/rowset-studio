package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/dbaopsio/rowset-studio/rowset-core/internal/engine"
)

// objectDDL returns the statement that creates a database object, so the
// schema browser can show what a table, view, routine or trigger really is.
func (s *Server) objectDDL(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	if !engine.EngineCapabilities(connection.Engine).DDL {
		writeError(w, http.StatusBadRequest, "UNSUPPORTED", "DDL viewing is not available for this engine")
		return
	}
	query := r.URL.Query()
	kind, schemaName, name := strings.TrimSpace(query.Get("kind")), strings.TrimSpace(query.Get("schema")), strings.TrimSpace(query.Get("name"))
	if name == "" || kind == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "kind and name are required")
		return
	}
	target, err := s.metadataEngineConnection(r.Context(), connection, strings.TrimSpace(query.Get("database")))
	if err != nil {
		writeError(w, http.StatusBadGateway, "EXEC_ERROR", err.Error())
		return
	}
	ctx, cancel := withConnectionTimeout(r, connection, 0, 60*time.Second)
	defer cancel()
	definition, err := s.engines.ObjectDDL(ctx, target, kind, schemaName, name)
	if err != nil {
		writeError(w, http.StatusBadGateway, "EXEC_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sql": definition})
}
