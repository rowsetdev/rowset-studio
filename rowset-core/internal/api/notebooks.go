package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/id"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

const (
	notebookLimit     = 1 << 20
	notebookMaxCells  = 500
	notebookCellBytes = 256 * 1024
)

// Notebooks hold notes and SQL cells. Cells are never executed here; Studio
// opens a SQL cell in an editor tab.
type notebookCell struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Content      string  `json:"content"`
	ConnectionID *string `json:"connectionId,omitempty"`
	Database     string  `json:"database,omitempty"`
}

type notebookDocument struct {
	Title string         `json:"title"`
	Cells []notebookCell `json:"cells"`
}

// The envelope binds ciphertext to its row and owner, so a copied ciphertext
// cannot be replayed under another notebook or user.
type notebookEnvelope struct {
	NotebookID string           `json:"notebookId"`
	UserID     string           `json:"userId"`
	OrgID      string           `json:"orgId"`
	Document   notebookDocument `json:"document"`
}

func validNotebook(doc *notebookDocument) bool {
	doc.Title = strings.TrimSpace(doc.Title)
	if doc.Title == "" || len(doc.Title) > 200 || len(doc.Cells) > notebookMaxCells {
		return false
	}
	if doc.Cells == nil {
		doc.Cells = []notebookCell{}
	}
	ids := make(map[string]bool, len(doc.Cells))
	for _, cell := range doc.Cells {
		if cell.ID == "" || len(cell.ID) > 128 || ids[cell.ID] || (cell.Kind != "markdown" && cell.Kind != "sql") ||
			len(cell.Content) > notebookCellBytes || len(cell.Database) > 512 || (cell.ConnectionID != nil && len(*cell.ConnectionID) > 128) {
			return false
		}
		ids[cell.ID] = true
	}
	return true
}

func (s *Server) sealNotebook(identity domain.Identity, notebookID string, doc notebookDocument) ([]byte, []byte, error) {
	if s.vault == nil {
		return nil, nil, errors.New("notebook encryption unavailable")
	}
	plain, err := json.Marshal(notebookEnvelope{NotebookID: notebookID, UserID: identity.UserID, OrgID: identity.OrgID, Document: doc})
	if err != nil {
		return nil, nil, err
	}
	return s.vault.Encrypt(plain)
}

func (s *Server) openNotebook(identity domain.Identity, item store.Notebook) (notebookDocument, error) {
	if s.vault == nil || len(item.Nonce) != 12 {
		return notebookDocument{}, errors.New("notebook encryption unavailable")
	}
	plain, err := s.vault.Decrypt(item.Ciphertext, item.Nonce)
	var envelope notebookEnvelope
	if err != nil || json.Unmarshal(plain, &envelope) != nil || envelope.NotebookID != item.ID || envelope.UserID != identity.UserID || envelope.OrgID != identity.OrgID || !validNotebook(&envelope.Document) {
		return notebookDocument{}, errors.New("notebook could not be decrypted or validated")
	}
	return envelope.Document, nil
}

// importSavedQueries converts a user's saved queries into one notebook the
// first time notebooks are listed. The saved queries themselves are kept.
func (s *Server) importSavedQueries(ctx context.Context, identity domain.Identity) {
	if done, err := s.store.SavedQueriesImported(ctx, identity.UserID); err != nil || done {
		return
	}
	queries, err := s.store.ListSavedQueries(ctx, identity.UserID)
	if err != nil || len(queries) == 0 {
		return
	}
	doc := notebookDocument{Title: "Saved queries", Cells: make([]notebookCell, 0, len(queries)*2)}
	for index := len(queries) - 1; index >= 0 && len(doc.Cells)+2 <= notebookMaxCells; index-- {
		query := queries[index]
		connectionID := query.ConnectionID
		doc.Cells = append(doc.Cells,
			notebookCell{ID: id.New(), Kind: "markdown", Content: "### " + query.Name},
			notebookCell{ID: id.New(), Kind: "sql", Content: query.SQL, ConnectionID: &connectionID})
	}
	notebookID := id.New()
	ciphertext, nonce, err := s.sealNotebook(identity, notebookID, doc)
	if err != nil {
		return
	}
	now := store.NowString()
	_, _ = s.store.CreateNotebookOnce(ctx, store.Notebook{ID: notebookID, OrgID: identity.OrgID, UserID: identity.UserID, Ciphertext: ciphertext, Nonce: nonce, CreatedAt: now, UpdatedAt: now})
}

func (s *Server) listNotebooks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	identity := identityFromContext(r.Context())
	s.importSavedQueries(r.Context(), identity)
	items, err := s.store.ListNotebooks(r.Context(), identity.UserID)
	if err != nil {
		writeError(w, 500, "NOTEBOOK_UNAVAILABLE", "failed to list notebooks")
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		entry := map[string]any{"id": item.ID, "revision": item.Revision, "updatedAt": item.UpdatedAt}
		if doc, err := s.openNotebook(identity, item); err != nil {
			entry["title"], entry["cellCount"], entry["unreadable"] = "Unreadable notebook", 0, true
		} else {
			entry["title"], entry["cellCount"] = doc.Title, len(doc.Cells)
		}
		out = append(out, entry)
	}
	writeJSON(w, 200, map[string]any{"notebooks": out})
}

func (s *Server) getNotebook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	identity := identityFromContext(r.Context())
	item, err := s.store.Notebook(r.Context(), r.PathValue("id"), identity.UserID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "NOT_FOUND", "notebook not found")
		return
	}
	if err != nil {
		writeError(w, 500, "NOTEBOOK_UNAVAILABLE", "notebook unavailable")
		return
	}
	doc, err := s.openNotebook(identity, item)
	if err != nil {
		writeError(w, 500, "NOTEBOOK_UNAVAILABLE", "Notebook could not be decrypted or validated; it has not been changed.")
		return
	}
	writeJSON(w, 200, map[string]any{"id": item.ID, "revision": item.Revision, "updatedAt": item.UpdatedAt, "document": doc})
}

func (s *Server) createNotebook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, notebookLimit)
	var input struct {
		Document notebookDocument `json:"document"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !validNotebook(&input.Document) {
		writeError(w, 400, "BAD_NOTEBOOK", "A notebook needs a title (up to 200 characters) and at most 500 uniquely identified markdown or sql cells.")
		return
	}
	identity := identityFromContext(r.Context())
	notebookID := id.New()
	ciphertext, nonce, err := s.sealNotebook(identity, notebookID, input.Document)
	if err != nil {
		writeError(w, 500, "NOTEBOOK_UNAVAILABLE", "notebook encryption failed")
		return
	}
	now := store.NowString()
	if err := s.store.CreateNotebook(r.Context(), store.Notebook{ID: notebookID, OrgID: identity.OrgID, UserID: identity.UserID, Ciphertext: ciphertext, Nonce: nonce, CreatedAt: now, UpdatedAt: now}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"id": notebookID, "revision": 1, "updatedAt": now, "document": input.Document})
}

func (s *Server) putNotebook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, notebookLimit)
	var input struct {
		Revision *int64           `json:"revision"`
		Document notebookDocument `json:"document"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Revision == nil || *input.Revision < 1 || !validNotebook(&input.Document) {
		writeError(w, 400, "BAD_NOTEBOOK", "Send the expected revision and a notebook with a title and at most 500 uniquely identified markdown or sql cells.")
		return
	}
	identity := identityFromContext(r.Context())
	notebookID := r.PathValue("id")
	if _, err := s.store.Notebook(r.Context(), notebookID, identity.UserID); errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "NOT_FOUND", "notebook not found")
		return
	} else if err != nil {
		writeError(w, 500, "NOTEBOOK_UNAVAILABLE", "notebook unavailable")
		return
	}
	ciphertext, nonce, err := s.sealNotebook(identity, notebookID, input.Document)
	if err != nil {
		writeError(w, 500, "NOTEBOOK_UNAVAILABLE", "notebook encryption failed")
		return
	}
	now := store.NowString()
	err = s.store.SaveNotebook(r.Context(), notebookID, identity.UserID, *input.Revision, ciphertext, nonce, now)
	if errors.Is(err, store.ErrNotebookConflict) {
		writeError(w, 409, "NOTEBOOK_CONFLICT", "This notebook was changed elsewhere. Reload it; nothing was overwritten.")
		return
	}
	if err != nil {
		writeError(w, 500, "NOTEBOOK_UNAVAILABLE", "notebook save failed")
		return
	}
	writeJSON(w, 200, map[string]any{"revision": *input.Revision + 1, "updatedAt": now})
}

func (s *Server) deleteNotebook(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteNotebook(r.Context(), r.PathValue("id"), identityFromContext(r.Context()).UserID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
