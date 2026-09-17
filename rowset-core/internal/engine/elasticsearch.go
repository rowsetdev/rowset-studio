package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	elasticsearch "github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
)

func elasticsearchClient(connection Connection) (*elasticsearch.Client, error) {
	scheme := "http"
	transport := http.DefaultTransport
	if connection.TLS.Mode != TLSDisable && connection.TLS.Mode != "" {
		config, err := tlsConfig(connection.TLS, connection.Host)
		if err != nil {
			return nil, err
		}
		scheme = "https"
		transport = &http.Transport{TLSClientConfig: config}
	}
	address := fmt.Sprintf("%s://%s:%d", scheme, connection.Host, connection.Port)
	cfg := elasticsearch.Config{
		Addresses: []string{address},
		Transport: transport,
	}
	if connection.Username != "" {
		cfg.Username, cfg.Password = connection.Username, connection.Password
	}
	return elasticsearch.NewClient(cfg)
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
	client, err := elasticsearchClient(connection)
	if err != nil {
		return err
	}
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
	client, err := elasticsearchClient(connection)
	if err != nil {
		return result, err
	}
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
	client, err := elasticsearchClient(connection)
	if err != nil {
		return "", err
	}
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
	client, err := elasticsearchClient(connection)
	if err != nil {
		return err
	}
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
	client, err := elasticsearchClient(connection)
	if err != nil {
		return err
	}
	res, err := client.Delete(input.Index, input.ID, client.Delete.WithContext(ctx))
	_, err = esRequest(ctx, client, res, err)
	return err
}

type ElasticsearchSearchInput struct {
	Index string          `json:"index"`
	Query json.RawMessage `json:"query"`
	Size  int             `json:"size"`
}

// ElasticsearchSearch runs a single search request body against one index.
// Only _search, _doc and _update are exposed (never _delete_by_query or
// other bulk/scripted write APIs); each call's guardrail statement gates
// whether it runs.
func (m *Manager) ElasticsearchSearch(ctx context.Context, connection Connection, input ElasticsearchSearchInput) ([]json.RawMessage, bool, error) {
	if strings.TrimSpace(input.Index) == "" {
		return nil, false, errors.New("index is required")
	}
	if input.Size <= 0 {
		input.Size = 100
	}
	if input.Size > 10000 {
		return nil, false, errors.New("size must be 10000 or less")
	}
	body := map[string]any{"size": input.Size}
	if len(input.Query) > 0 {
		var query any
		if err := json.Unmarshal(input.Query, &query); err != nil {
			return nil, false, fmt.Errorf("query must be a JSON object: %w", err)
		}
		body["query"] = query
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, false, err
	}
	client, err := elasticsearchClient(connection)
	if err != nil {
		return nil, false, err
	}
	res, err := client.Search(
		client.Search.WithContext(ctx),
		client.Search.WithIndex(input.Index),
		client.Search.WithBody(bytes.NewReader(encoded)),
	)
	decoded, err := esRequest(ctx, client, res, err)
	if err != nil {
		return nil, false, err
	}
	hitsWrap, _ := decoded["hits"].(map[string]any)
	hits, _ := hitsWrap["hits"].([]any)
	docs := make([]json.RawMessage, 0, len(hits))
	for _, hit := range hits {
		raw, err := json.Marshal(hit)
		if err != nil {
			continue
		}
		docs = append(docs, raw)
	}
	truncated := len(docs) == input.Size
	return docs, truncated, nil
}
