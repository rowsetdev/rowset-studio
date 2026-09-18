package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	elasticsearch "github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
	"golang.org/x/crypto/ssh"
)

// elasticsearchClient opens a client and returns the cleanup that closes its
// transport's idle connections and its SSH tunnel, if any. Every caller must
// defer the cleanup. Each HTTP request the client makes opens a fresh
// connection through the tunnel (not a driver-level ssh.Client leak, since
// the tunnel is only closed once the whole call finishes).
func elasticsearchClient(ctx context.Context, connection Connection) (*elasticsearch.Client, func(), error) {
	scheme := "http"
	var tlsCfg *tls.Config
	if connection.TLS.Mode != TLSDisable && connection.TLS.Mode != "" {
		config, err := tlsConfig(connection.TLS, connection.Host)
		if err != nil {
			return nil, nil, err
		}
		scheme = "https"
		tlsCfg = config
	}
	var tunnel *ssh.Client
	var err error
	transport := &http.Transport{TLSClientConfig: tlsCfg}
	if connection.SSH.enabled() {
		tunnel, err = dialSSH(ctx, connection.SSH, nil)
		if err != nil {
			return nil, nil, err
		}
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return tunnelDial(ctx, tunnel, network, addr)
		}
	}
	address := fmt.Sprintf("%s://%s:%d", scheme, connection.Host, connection.Port)
	cfg := elasticsearch.Config{
		Addresses: []string{address},
		Transport: transport,
	}
	if connection.Username != "" {
		cfg.Username, cfg.Password = connection.Username, connection.Password
	}
	client, err := elasticsearch.NewClient(cfg)
	if err != nil {
		if tunnel != nil {
			_ = tunnel.Close()
		}
		return nil, nil, err
	}
	cleanup := func() {
		transport.CloseIdleConnections()
		if tunnel != nil {
			_ = tunnel.Close()
		}
	}
	return client, cleanup, nil
}

func esRequest(ctx context.Context, client *elasticsearch.Client, res *esapi.Response, err error) (map[string]any, error) {
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.IsError() {
		return nil, fmt.Errorf("elasticsearch: %s", strings.TrimSpace(string(body)))
	}
	var decoded map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, err
		}
	}
	return decoded, nil
}

func elasticsearchTest(ctx context.Context, connection Connection) error {
	client, cleanup, err := elasticsearchClient(ctx, connection)
	if err != nil {
		return err
	}
	defer cleanup()
	res, err := client.Ping(client.Ping.WithContext(ctx))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.IsError() {
		return fmt.Errorf("elasticsearch ping failed: %s", res.Status())
	}
	return nil
}

// elasticsearchDatabases has no real analogue; Rowset treats the single
// cluster as one database, matching how file engines report themselves.
func elasticsearchDatabases(ctx context.Context, connection Connection) ([]string, error) {
	name := connection.Database
	if strings.TrimSpace(name) == "" {
		name = "elasticsearch"
	}
	return []string{name}, nil
}

// elasticsearchSchema reports each index as a pseudo-table with its mapped
// fields as columns, the same shape MongoDB's collections use.
func elasticsearchSchema(ctx context.Context, connection Connection) (Schema, error) {
	result := Schema{Tables: map[string][]Column{}, Indexes: map[string][]Index{}, Views: map[string]bool{}}
	client, cleanup, err := elasticsearchClient(ctx, connection)
	if err != nil {
		return result, err
	}
	defer cleanup()
	res, err := client.Indices.GetMapping(client.Indices.GetMapping.WithContext(ctx))
	decoded, err := esRequest(ctx, client, res, err)
	if err != nil {
		return result, err
	}
	dbName := connection.Database
	if strings.TrimSpace(dbName) == "" {
		dbName = "elasticsearch"
	}
	for index, raw := range decoded {
		if strings.HasPrefix(index, ".") {
			continue // system/internal indices
		}
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		mappings, _ := entry["mappings"].(map[string]any)
		properties, _ := mappings["properties"].(map[string]any)
		key := tableKey(dbName, index)
		if len(properties) == 0 {
			result.Tables[key] = []Column{}
			continue
		}
		for field, def := range properties {
			fieldType := "object"
			if defMap, ok := def.(map[string]any); ok {
				if t, ok := defMap["type"].(string); ok {
					fieldType = t
				}
			}
			result.Tables[key] = append(result.Tables[key], Column{Schema: dbName, Table: index, Name: field, DataType: fieldType})
		}
	}
	return result, nil
}

type ElasticsearchIndexInput struct {
	Index    string          `json:"index"`
	ID       string          `json:"id"`
	Document json.RawMessage `json:"document"`
}

// ElasticsearchIndex creates or fully replaces one document at index/id.
func (m *Manager) ElasticsearchIndex(ctx context.Context, connection Connection, input ElasticsearchIndexInput) (string, error) {
	if strings.TrimSpace(input.Index) == "" || strings.TrimSpace(input.ID) == "" {
		return "", errors.New("index and id are required")
	}
	if len(input.Document) == 0 {
		return "", errors.New("document is required")
	}
	var probe any
	if err := json.Unmarshal(input.Document, &probe); err != nil {
		return "", fmt.Errorf("document must be a JSON object: %w", err)
	}
	client, cleanup, err := elasticsearchClient(ctx, connection)
	if err != nil {
		return "", err
	}
	defer cleanup()
	res, err := client.Index(input.Index, bytes.NewReader(input.Document), client.Index.WithContext(ctx), client.Index.WithDocumentID(input.ID))
	decoded, err := esRequest(ctx, client, res, err)
	if err != nil {
		return "", err
	}
	id, _ := decoded["_id"].(string)
	return id, nil
}

type ElasticsearchUpdateInput struct {
	Index string          `json:"index"`
	ID    string          `json:"id"`
	Doc   json.RawMessage `json:"doc"`
}

// ElasticsearchUpdate merges fields into an existing document via _update.
func (m *Manager) ElasticsearchUpdate(ctx context.Context, connection Connection, input ElasticsearchUpdateInput) error {
	if strings.TrimSpace(input.Index) == "" || strings.TrimSpace(input.ID) == "" {
		return errors.New("index and id are required")
	}
	if len(input.Doc) == 0 {
		return errors.New("doc is required")
	}
	var probe any
	if err := json.Unmarshal(input.Doc, &probe); err != nil {
		return fmt.Errorf("doc must be a JSON object: %w", err)
	}
	body, err := json.Marshal(map[string]json.RawMessage{"doc": input.Doc})
	if err != nil {
		return err
	}
	client, cleanup, err := elasticsearchClient(ctx, connection)
	if err != nil {
		return err
	}
	defer cleanup()
	res, err := client.Update(input.Index, input.ID, bytes.NewReader(body), client.Update.WithContext(ctx))
	_, err = esRequest(ctx, client, res, err)
	return err
}

type ElasticsearchDeleteInput struct {
	Index string `json:"index"`
	ID    string `json:"id"`
}

// ElasticsearchDelete removes one document by index/id.
func (m *Manager) ElasticsearchDelete(ctx context.Context, connection Connection, input ElasticsearchDeleteInput) error {
	if strings.TrimSpace(input.Index) == "" || strings.TrimSpace(input.ID) == "" {
		return errors.New("index and id are required")
	}
	client, cleanup, err := elasticsearchClient(ctx, connection)
	if err != nil {
		return err
	}
	defer cleanup()
	res, err := client.Delete(input.Index, input.ID, client.Delete.WithContext(ctx))
	_, err = esRequest(ctx, client, res, err)
	return err
}

// ElasticsearchGet fetches one document's current _source, for row-backup
// capture before an update or delete. exists is false on a 404, which is
// not an error: the document simply isn't there to back up.
func (m *Manager) ElasticsearchGet(ctx context.Context, connection Connection, index, id string) (source json.RawMessage, exists bool, err error) {
	client, cleanup, err := elasticsearchClient(ctx, connection)
	if err != nil {
		return nil, false, err
	}
	defer cleanup()
	res, err := client.Get(index, id, client.Get.WithContext(ctx))
	if err != nil {
		return nil, false, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, false, err
	}
	if res.IsError() {
		return nil, false, fmt.Errorf("elasticsearch: %s", strings.TrimSpace(string(body)))
	}
	var decoded struct {
		Source json.RawMessage `json:"_source"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, false, err
	}
	return decoded.Source, true, nil
}

type ElasticsearchSearchInput struct {
	Index string          `json:"index"`
	Query json.RawMessage `json:"query"`
	// Sort orders hits and, together with SearchAfter, pages past size/10000
	// without deep pagination's from+size cost or its 10000-result limit.
	Sort json.RawMessage `json:"sort"`
	// SearchAfter continues from the last page's sort values (its response's
	// SearchAfter field); it requires Sort to be set the same way each page.
	SearchAfter json.RawMessage `json:"searchAfter"`
	// Aggs runs alongside the query; its result comes back verbatim.
	Aggs json.RawMessage `json:"aggs"`
	Size int             `json:"size"`
}

type ElasticsearchSearchResult struct {
	Documents    []json.RawMessage `json:"documents"`
	Truncated    bool              `json:"truncated"`
	Aggregations json.RawMessage   `json:"aggregations,omitempty"`
	// SearchAfter is the last hit's sort values; pass it back as the next
	// page's SearchAfter to continue, when Sort was set on this call.
	SearchAfter json.RawMessage `json:"searchAfter,omitempty"`
}

// ElasticsearchSearch runs a single search request body against one index.
// Only _search, _doc and _update are exposed (never _delete_by_query or
// other bulk/scripted write APIs); each call's guardrail statement gates
// whether it runs.
func (m *Manager) ElasticsearchSearch(ctx context.Context, connection Connection, input ElasticsearchSearchInput) (ElasticsearchSearchResult, error) {
	if strings.TrimSpace(input.Index) == "" {
		return ElasticsearchSearchResult{}, errors.New("index is required")
	}
	if input.Size <= 0 {
		input.Size = 100
	}
	if input.Size > 10000 {
		return ElasticsearchSearchResult{}, errors.New("size must be 10000 or less")
	}
	if len(input.SearchAfter) > 0 && len(input.Sort) == 0 {
		return ElasticsearchSearchResult{}, errors.New("searchAfter requires sort")
	}
	body := map[string]any{"size": input.Size}
	for _, field := range []struct {
		name  string
		value json.RawMessage
	}{{"query", input.Query}, {"sort", input.Sort}, {"search_after", input.SearchAfter}, {"aggs", input.Aggs}} {
		if len(field.value) == 0 {
			continue
		}
		var decoded any
		if err := json.Unmarshal(field.value, &decoded); err != nil {
			return ElasticsearchSearchResult{}, fmt.Errorf("%s must be valid JSON: %w", field.name, err)
		}
		body[field.name] = decoded
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return ElasticsearchSearchResult{}, err
	}
	client, cleanup, err := elasticsearchClient(ctx, connection)
	if err != nil {
		return ElasticsearchSearchResult{}, err
	}
	defer cleanup()
	res, err := client.Search(
		client.Search.WithContext(ctx),
		client.Search.WithIndex(input.Index),
		client.Search.WithBody(bytes.NewReader(encoded)),
	)
	decoded, err := esRequest(ctx, client, res, err)
	if err != nil {
		return ElasticsearchSearchResult{}, err
	}
	hitsWrap, _ := decoded["hits"].(map[string]any)
	hits, _ := hitsWrap["hits"].([]any)
	docs := make([]json.RawMessage, 0, len(hits))
	var lastSort json.RawMessage
	for _, hit := range hits {
		raw, err := json.Marshal(hit)
		if err != nil {
			continue
		}
		docs = append(docs, raw)
		if hitMap, ok := hit.(map[string]any); ok {
			if sort, ok := hitMap["sort"]; ok {
				if sortRaw, err := json.Marshal(sort); err == nil {
					lastSort = sortRaw
				}
			}
		}
	}
	result := ElasticsearchSearchResult{Documents: docs, Truncated: len(docs) == input.Size, SearchAfter: lastSort}
	if aggs, ok := decoded["aggregations"]; ok {
		if raw, err := json.Marshal(aggs); err == nil {
			result.Aggregations = raw
		}
	}
	return result, nil
}
