package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	goredis "github.com/redis/go-redis/v9"
)

func redisClient(connection Connection) (*goredis.Client, error) {
	db := 0
	if strings.TrimSpace(connection.Database) != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(connection.Database))
		if err != nil || parsed < 0 || parsed > 15 {
			return nil, errors.New("database must be a Redis DB index between 0 and 15")
		}
		db = parsed
	}
	tls, err := tlsConfig(connection.TLS, connection.Host)
	if err != nil {
		return nil, err
	}
	return goredis.NewClient(&goredis.Options{
		Addr:        net.JoinHostPort(connection.Host, fmt.Sprint(connection.Port)),
		Username:    connection.Username,
		Password:    connection.Password,
		DB:          db,
		TLSConfig:   tls,
		DialTimeout: 10 * time.Second,
		ReadTimeout: 24 * time.Hour,
	}), nil
}

func redisTest(ctx context.Context, connection Connection) error {
	client, err := redisClient(connection)
	if err != nil {
		return err
	}
	defer client.Close()
	return client.Ping(ctx).Err()
}

// redisDatabases lists the numbered DBs (0-15) that INFO keyspace reports as
// non-empty, always including the connection's own selected DB.
func redisDatabases(ctx context.Context, connection Connection) ([]string, error) {
	client, err := redisClient(connection)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	info, err := client.Info(ctx, "keyspace").Result()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var result []string
	for _, line := range strings.Split(info, "\r\n") {
		if !strings.HasPrefix(line, "db") {
			continue
		}
		name := line[:strings.Index(line, ":")]
		name = strings.TrimPrefix(name, "db")
		if !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}
	self := strconv.Itoa(int(client.Options().DB))
	if !seen[self] {
		result = append([]string{self}, result...)
	}
	return result, nil
}

// redisSchema groups a sample of keys by their Redis type (string, hash,
// list, set, zset, stream) as pseudo-tables, since Redis itself has no
// schema. Scanning is capped so a very large keyspace stays responsive.
func redisSchema(ctx context.Context, connection Connection) (Schema, error) {
	result := Schema{Tables: map[string][]Column{}, Indexes: map[string][]Index{}, Views: map[string]bool{}}
	client, err := redisClient(connection)
	if err != nil {
		return result, err
	}
	defer client.Close()
	dbName := strconv.Itoa(int(client.Options().DB))
	types := map[string]int{}
	var cursor uint64
	scanned := 0
	for {
		keys, next, err := client.Scan(ctx, cursor, "*", 500).Result()
		if err != nil {
			return result, err
		}
		for _, key := range keys {
			kind, err := client.Type(ctx, key).Result()
			if err != nil {
				continue
			}
			types[kind]++
		}
		scanned += len(keys)
		cursor = next
		if cursor == 0 || scanned >= 5000 {
			break
		}
	}
	if scanned >= 5000 {
		result.Warnings = append(result.Warnings, "Key types are sampled from the first 5000 keys scanned; the full keyspace may contain other types.")
	}
	for kind, count := range types {
		key := tableKey(dbName, kind)
		result.Tables[key] = []Column{{Schema: dbName, Table: kind, Name: "key", DataType: "string"}, {Schema: dbName, Table: kind, Name: "value", DataType: kind}, {Schema: dbName, Table: kind, Name: "ttl", DataType: "seconds"}}
		_ = count
	}
	return result, nil
}

type RedisScanInput struct {
	Database string `json:"database"`
	Pattern  string `json:"pattern"`
	Type     string `json:"type"`
	Cursor   uint64 `json:"cursor"`
	Limit    int    `json:"limit"`
}

type RedisEntry struct {
	Key   string          `json:"key"`
	Type  string          `json:"type"`
	TTL   int64           `json:"ttl"`
	Value json.RawMessage `json:"value"`
}

// RedisScan lists keys matching a glob pattern (SCAN, never KEYS, so a large
// keyspace never blocks the server) and previews each value, truncated so a
// huge collection or string cannot flood the response.
func (m *Manager) RedisScan(ctx context.Context, connection Connection, input RedisScanInput) ([]RedisEntry, uint64, error) {
	if input.Limit <= 0 {
		input.Limit = 100
	}
	if input.Limit > 1000 {
		return nil, 0, errors.New("limit must be 1000 or less")
	}
	pattern := input.Pattern
	if strings.TrimSpace(pattern) == "" {
		pattern = "*"
	}
	client, err := redisClient(connection)
	if err != nil {
		return nil, 0, err
	}
	defer client.Close()
	var entries []RedisEntry
	cursor := input.Cursor
	for len(entries) < input.Limit {
		keys, next, err := client.Scan(ctx, cursor, pattern, int64(input.Limit)).Result()
		if err != nil {
			return nil, 0, err
		}
		for _, key := range keys {
			kind, err := client.Type(ctx, key).Result()
			if err != nil || kind == "none" {
				continue
			}
			if input.Type != "" && kind != input.Type {
				continue
			}
			ttl, _ := client.TTL(ctx, key).Result()
			value, err := redisPreview(ctx, client, key, kind)
			if err != nil {
				value = json.RawMessage(fmt.Sprintf("%q", "error: "+err.Error()))
			}
			entries = append(entries, RedisEntry{Key: key, Type: kind, TTL: int64(ttl / time.Second), Value: value})
			if len(entries) == input.Limit {
				break
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return entries, cursor, nil
}

type RedisWriteInput struct {
	Database   string `json:"database"`
	Key        string `json:"key"`
	Type       string `json:"type"`  // "string" or "hash"
	Field      string `json:"field"` // required when type is "hash"
	Value      string `json:"value"`
	TTLSeconds int    `json:"ttlSeconds"` // 0 leaves any existing TTL untouched for hash; string SET clears it unless positive
}

// RedisWrite sets a string value or one hash field. Only these two types are
// supported for now; list/set/zset/stream editing is not exposed yet.
func (m *Manager) RedisWrite(ctx context.Context, connection Connection, input RedisWriteInput) error {
	if strings.TrimSpace(input.Key) == "" {
		return errors.New("key is required")
	}
	if input.TTLSeconds < 0 {
		return errors.New("ttlSeconds must be zero or positive")
	}
	client, err := redisClient(connection)
	if err != nil {
		return err
	}
	defer client.Close()
	switch input.Type {
	case "string":
		ttl := time.Duration(input.TTLSeconds) * time.Second
		return client.Set(ctx, input.Key, input.Value, ttl).Err()
	case "hash":
		if strings.TrimSpace(input.Field) == "" {
			return errors.New("field is required for hash writes")
		}
		if err := client.HSet(ctx, input.Key, input.Field, input.Value).Err(); err != nil {
			return err
		}
		if input.TTLSeconds > 0 {
			return client.Expire(ctx, input.Key, time.Duration(input.TTLSeconds)*time.Second).Err()
		}
		return nil
	default:
		return fmt.Errorf("writing a %q key is not supported yet; only string and hash are", input.Type)
	}
}

type RedisDeleteInput struct {
	Database string `json:"database"`
	Key      string `json:"key"`
}

// RedisDelete removes a key regardless of its type.
func (m *Manager) RedisDelete(ctx context.Context, connection Connection, input RedisDeleteInput) (int64, error) {
	if strings.TrimSpace(input.Key) == "" {
		return 0, errors.New("key is required")
	}
	client, err := redisClient(connection)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	return client.Del(ctx, input.Key).Result()
}

// RedisSnapshot is a key's exact state, for row-backup capture/restore. The
// value is DUMP's own serialization (Redis's internal RDB-like encoding),
// not a per-type preview: it round-trips any key type byte-for-byte,
// including its exact encoding, unlike parsing hash/list/set members back
// into literals would.
type RedisSnapshot struct {
	Existed bool
	Dump    string // base64 of the DUMP payload; empty when Existed is false
	TTLMs   int64  // 0 means no expiry
}

// RedisSnapshotKey captures a key's current state before a write or delete
// changes or removes it.
func (m *Manager) RedisSnapshotKey(ctx context.Context, connection Connection, key string) (RedisSnapshot, error) {
	client, err := redisClient(connection)
	if err != nil {
		return RedisSnapshot{}, err
	}
	defer client.Close()
	dump, err := client.Dump(ctx, key).Result()
	if err != nil {
		if err == goredis.Nil {
			return RedisSnapshot{Existed: false}, nil
		}
		return RedisSnapshot{}, err
	}
	ttl, err := client.PTTL(ctx, key).Result()
	if err != nil {
		return RedisSnapshot{}, err
	}
	var ms int64
	if ttl > 0 {
		ms = ttl.Milliseconds()
	}
	return RedisSnapshot{Existed: true, Dump: base64.StdEncoding.EncodeToString([]byte(dump)), TTLMs: ms}, nil
}

// RedisRestoreSnapshot puts a key back exactly as RedisSnapshotKey captured
// it: deleted if it did not exist yet, or restored with RESTORE (its exact
// bytes and TTL) otherwise.
func (m *Manager) RedisRestoreSnapshot(ctx context.Context, connection Connection, key string, snapshot RedisSnapshot) error {
	client, err := redisClient(connection)
	if err != nil {
		return err
	}
	defer client.Close()
	if !snapshot.Existed {
		return client.Del(ctx, key).Err()
	}
	raw, err := base64.StdEncoding.DecodeString(snapshot.Dump)
	if err != nil {
		return err
	}
	return client.RestoreReplace(ctx, key, time.Duration(snapshot.TTLMs)*time.Millisecond, string(raw)).Err()
}

func redisPreview(ctx context.Context, client *goredis.Client, key, kind string) (json.RawMessage, error) {
	const capItems = 100
	switch kind {
	case "string":
		v, err := client.GetRange(ctx, key, 0, 16383).Result()
		if err != nil {
			return nil, err
		}
		for len(v) > 0 && !utf8.ValidString(v) {
			v = v[:len(v)-1]
		}
		if len(v) > 4096 {
			v = string([]rune(v)[:min(len([]rune(v)), 4096)]) + "…"
		}
		raw, _ := json.Marshal(v)
		return raw, nil
	case "hash":
		v, _, err := client.HScan(ctx, key, 0, "*", capItems).Result()
		if err != nil {
			return nil, err
		}
		preview := make(map[string]string, min(capItems, len(v)/2))
		for i := 0; i+1 < len(v) && len(preview) < capItems; i += 2 {
			preview[redisPreviewText(v[i], 256)] = redisPreviewText(v[i+1], 4096)
		}
		raw, _ := json.Marshal(preview)
		return raw, nil
	case "list":
		v, err := client.LRange(ctx, key, 0, capItems-1).Result()
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(v)
		return raw, nil
	case "set":
		v, err := client.SRandMemberN(ctx, key, capItems).Result()
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(v)
		return raw, nil
	case "zset":
		v, err := client.ZRangeWithScores(ctx, key, 0, capItems-1).Result()
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(v)
		return raw, nil
	case "stream":
		v, err := client.XRevRangeN(ctx, key, "+", "-", capItems).Result()
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(v)
		return raw, nil
	default:
		return json.RawMessage(fmt.Sprintf("%q", "unsupported type: "+kind)), nil
	}
}

func redisPreviewText(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "…"
}
