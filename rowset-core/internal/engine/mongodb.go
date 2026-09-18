package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"golang.org/x/crypto/ssh"
)

// mongoSSHDialer forwards every address the driver dials (the seed host and
// any replica set member discovered afterward) through one SSH tunnel.
type mongoSSHDialer struct{ client *ssh.Client }

func (d mongoSSHDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return tunnelDial(ctx, d.client, network, addr)
}

// mongoClient opens a client and returns the cleanup that closes it and its
// SSH tunnel, if any. Every caller must defer the cleanup.
func mongoClient(ctx context.Context, connection Connection) (*mongo.Client, func(), error) {
	tls, err := tlsConfig(connection.TLS, connection.Host)
	if err != nil {
		return nil, nil, err
	}
	opts := options.Client().SetHosts([]string{net.JoinHostPort(connection.Host, fmt.Sprint(connection.Port))}).SetConnectTimeout(10 * time.Second).SetServerSelectionTimeout(10 * time.Second)
	if tls != nil {
		opts.SetTLSConfig(tls)
	}
	if connection.Username != "" {
		opts.SetAuth(options.Credential{AuthSource: "admin", Username: connection.Username, Password: connection.Password})
	}
	var tunnel *ssh.Client
	if connection.SSH.enabled() {
		tunnel, err = dialSSH(ctx, connection.SSH, nil)
		if err != nil {
			return nil, nil, err
		}
		opts.SetDialer(mongoSSHDialer{client: tunnel})
	}
	client, err := mongo.Connect(opts)
	if err != nil {
		if tunnel != nil {
			_ = tunnel.Close()
		}
		return nil, nil, err
	}
	cleanup := func() {
		closeMongo(client)
		if tunnel != nil {
			_ = tunnel.Close()
		}
	}
	return client, cleanup, nil
}
func closeMongo(client *mongo.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = client.Disconnect(ctx)
}
func mongoTest(ctx context.Context, connection Connection) error {
	client, cleanup, err := mongoClient(ctx, connection)
	if err != nil {
		return err
	}
	defer cleanup()
	return client.Ping(ctx, readpref.Primary())
}
func mongoDatabases(ctx context.Context, connection Connection) ([]string, error) {
	client, cleanup, err := mongoClient(ctx, connection)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return client.ListDatabaseNames(ctx, bson.D{})
}
func mongoSchema(ctx context.Context, connection Connection) (Schema, error) {
	result := Schema{Tables: map[string][]Column{}, Indexes: map[string][]Index{}, Views: map[string]bool{}}
	client, cleanup, err := mongoClient(ctx, connection)
	if err != nil {
		return result, err
	}
	defer cleanup()
	names, err := client.Database(connection.Database).ListCollectionNames(ctx, bson.D{})
	if err != nil {
		return result, err
	}
	for _, name := range names {
		result.Tables[tableKey(connection.Database, name)] = []Column{{Schema: connection.Database, Table: name, Name: "document", DataType: "BSON", Nullable: false}}
	}
	return result, nil
}

type MongoFindInput struct {
	Database   string          `json:"database"`
	Collection string          `json:"collection"`
	Filter     json.RawMessage `json:"filter"`
	Project    json.RawMessage `json:"project"`
	Sort       json.RawMessage `json:"sort"`
	Skip       int             `json:"skip"`
	Limit      int             `json:"limit"`
	MaxTimeMs  int64           `json:"maxTimeMs"`
}

func ParseMongoFilter(raw json.RawMessage) (bson.D, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var filter bson.D
	if err := bson.UnmarshalExtJSON(raw, false, &filter); err != nil {
		return nil, fmt.Errorf("filter must be an Extended JSON object: %w", err)
	}
	// This explorer only finds documents; server-side JavaScript is not needed.
	var reject func(any) bool
	reject = func(v any) bool {
		switch x := v.(type) {
		case bson.D:
			for _, e := range x {
				if e.Key == "$where" || e.Key == "$function" || e.Key == "$accumulator" || reject(e.Value) {
					return true
				}
			}
		case bson.A:
			for _, e := range x {
				if reject(e) {
					return true
				}
			}
		}
		return false
	}
	if reject(filter) {
		return nil, errors.New("server-side JavaScript is not supported in document filters")
	}
	return filter, nil
}

type MongoInsertInput struct {
	Database   string          `json:"database"`
	Collection string          `json:"collection"`
	Document   json.RawMessage `json:"document"`
}

// MongoInsertOne inserts one document, returning its _id as Extended JSON.
func (m *Manager) MongoInsertOne(ctx context.Context, connection Connection, input MongoInsertInput) (string, error) {
	client, cleanup, err := mongoClient(ctx, connection)
	if err != nil {
		return "", err
	}
	defer cleanup()
	return mongoInsertOneWith(ctx, client, connection.Database, input)
}

func mongoInsertOneWith(ctx context.Context, client *mongo.Client, database string, input MongoInsertInput) (string, error) {
	if strings.TrimSpace(input.Collection) == "" {
		return "", errors.New("collection is required")
	}
	var document bson.D
	if err := bson.UnmarshalExtJSON(input.Document, false, &document); err != nil {
		return "", fmt.Errorf("document must be an Extended JSON object: %w", err)
	}
	result, err := client.Database(database).Collection(input.Collection).InsertOne(ctx, document)
	if err != nil {
		return "", err
	}
	return marshalExtJSONValue(result.InsertedID)
}

// marshalExtJSONValue Extended-JSON-encodes a single BSON value (as opposed
// to a whole document). bson.MarshalExtJSON only writes documents; asked to
// write a bare scalar like an ObjectID at the top level, the value writer
// errors ("... positioned on a TopLevel") instead of producing JSON, so the
// value is wrapped in a one-field document and unwrapped again here.
func marshalExtJSONValue(value any) (string, error) {
	wrapped, err := bson.MarshalExtJSON(bson.D{{Key: "v", Value: value}}, true, false)
	if err != nil {
		return "", err
	}
	var parsed struct {
		V json.RawMessage `json:"v"`
	}
	if err := json.Unmarshal(wrapped, &parsed); err != nil {
		return "", err
	}
	return string(parsed.V), nil
}

type MongoUpdateInput struct {
	Database   string          `json:"database"`
	Collection string          `json:"collection"`
	Filter     json.RawMessage `json:"filter"`
	Update     json.RawMessage `json:"update"`
}

// MongoUpdateOne applies an update-operator document ($set, $unset, ...) to
// the first document matching filter. A full-document replacement is
// rejected: every operator update stays reviewable field by field.
func (m *Manager) MongoUpdateOne(ctx context.Context, connection Connection, input MongoUpdateInput) (int64, int64, error) {
	client, cleanup, err := mongoClient(ctx, connection)
	if err != nil {
		return 0, 0, err
	}
	defer cleanup()
	return mongoUpdateOneWith(ctx, client, connection.Database, input)
}

func mongoUpdateOneWith(ctx context.Context, client *mongo.Client, database string, input MongoUpdateInput) (int64, int64, error) {
	if strings.TrimSpace(input.Collection) == "" {
		return 0, 0, errors.New("collection is required")
	}
	filter, err := ParseMongoFilter(input.Filter)
	if err != nil {
		return 0, 0, err
	}
	if !mongoHasPredicate(filter) {
		return 0, 0, errors.New("update requires a non-empty filter")
	}
	var update bson.D
	if err := bson.UnmarshalExtJSON(input.Update, false, &update); err != nil {
		return 0, 0, fmt.Errorf("update must be an Extended JSON object: %w", err)
	}
	if len(update) == 0 {
		return 0, 0, errors.New("update is required")
	}
	for _, entry := range update {
		if !strings.HasPrefix(entry.Key, "$") {
			return 0, 0, errors.New("update must use operators such as $set, $unset or $inc; a full-document replacement is not supported")
		}
	}
	result, err := client.Database(database).Collection(input.Collection).UpdateOne(ctx, filter, update)
	if err != nil {
		return 0, 0, err
	}
	return result.MatchedCount, result.ModifiedCount, nil
}

// MongoReplaceInput is used only for row-backup restore: putting a document
// back exactly as it was captured, not for general use, which is why it
// skips MongoUpdateOne's "operators only" rule that protects hand-typed
// writes from an accidental full-document replacement.
type MongoReplaceInput struct {
	Database   string          `json:"database"`
	Collection string          `json:"collection"`
	Filter     json.RawMessage `json:"filter"`
	Document   json.RawMessage `json:"document"`
}

func (m *Manager) MongoReplaceOne(ctx context.Context, connection Connection, input MongoReplaceInput) error {
	client, cleanup, err := mongoClient(ctx, connection)
	if err != nil {
		return err
	}
	defer cleanup()
	return mongoReplaceOneWith(ctx, client, connection.Database, input)
}

func mongoReplaceOneWith(ctx context.Context, client *mongo.Client, database string, input MongoReplaceInput) error {
	if strings.TrimSpace(input.Collection) == "" {
		return errors.New("collection is required")
	}
	filter, err := ParseMongoFilter(input.Filter)
	if err != nil {
		return err
	}
	if !mongoHasPredicate(filter) {
		return errors.New("replace requires a non-empty filter")
	}
	var document bson.D
	if err := bson.UnmarshalExtJSON(input.Document, false, &document); err != nil {
		return fmt.Errorf("document must be an Extended JSON object: %w", err)
	}
	_, err = client.Database(database).Collection(input.Collection).ReplaceOne(ctx, filter, document, options.Replace().SetUpsert(true))
	return err
}

type MongoDeleteInput struct {
	Database   string          `json:"database"`
	Collection string          `json:"collection"`
	Filter     json.RawMessage `json:"filter"`
}

// MongoDeleteOne removes the first document matching filter.
func (m *Manager) MongoDeleteOne(ctx context.Context, connection Connection, input MongoDeleteInput) (int64, error) {
	client, cleanup, err := mongoClient(ctx, connection)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	return mongoDeleteOneWith(ctx, client, connection.Database, input)
}

func mongoDeleteOneWith(ctx context.Context, client *mongo.Client, database string, input MongoDeleteInput) (int64, error) {
	if strings.TrimSpace(input.Collection) == "" {
		return 0, errors.New("collection is required")
	}
	filter, err := ParseMongoFilter(input.Filter)
	if err != nil {
		return 0, err
	}
	if !mongoHasPredicate(filter) {
		return 0, errors.New("delete requires a non-empty filter")
	}
	result, err := client.Database(database).Collection(input.Collection).DeleteOne(ctx, filter)
	if err != nil {
		return 0, err
	}
	return result.DeletedCount, nil
}

// mongoHasPredicate reports whether filter restricts anything, the same
// conservative rule the API guardrail statement uses for reads.
func mongoHasPredicate(filter bson.D) bool {
	for _, entry := range filter {
		if !strings.HasPrefix(entry.Key, "$") {
			return true
		}
		if clauses, ok := entry.Value.(bson.A); ok && len(clauses) > 0 {
			return true
		}
	}
	return false
}

// MongoTransaction pins one client and session across several document
// writes. MongoDB only accepts a transaction number on a replica set member
// or mongos; a standalone mongod rejects the first operation inside it with
// a clear driver error ("Transaction numbers are only allowed on a replica
// set member or mongos"), which callers should surface as-is rather than
// translate, since it already explains the fix.
type MongoTransaction struct {
	client   *mongo.Client
	cleanup  func()
	session  *mongo.Session
	sessCtx  context.Context
	cancel   context.CancelFunc
	database string
	finish   sync.Once
	finished bool
	mu       sync.Mutex
}

// MongoBegin starts a session and a transaction on it.
func (m *Manager) MongoBegin(ctx context.Context, connection Connection) (*MongoTransaction, error) {
	client, cleanup, err := mongoClient(ctx, connection)
	if err != nil {
		return nil, err
	}
	session, err := client.StartSession()
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := session.StartTransaction(); err != nil {
		session.EndSession(context.Background())
		cleanup()
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &MongoTransaction{
		client:   client,
		cleanup:  cleanup,
		session:  session,
		sessCtx:  mongo.NewSessionContext(lifetime, session),
		cancel:   cancel,
		database: connection.Database,
	}, nil
}

func (t *MongoTransaction) InsertOne(input MongoInsertInput) (string, error) {
	return mongoInsertOneWith(t.sessCtx, t.client, t.database, input)
}

func (t *MongoTransaction) UpdateOne(input MongoUpdateInput) (int64, int64, error) {
	return mongoUpdateOneWith(t.sessCtx, t.client, t.database, input)
}

func (t *MongoTransaction) DeleteOne(input MongoDeleteInput) (int64, error) {
	return mongoDeleteOneWith(t.sessCtx, t.client, t.database, input)
}

// FindMany reads inside the transaction's own session, for row-backup
// capture: it must see the pre-write state consistently with the write that
// follows it in the same transaction, not a separate connection's view.
func (t *MongoTransaction) FindMany(input MongoFindInput) ([]json.RawMessage, bool, error) {
	return mongoFindWith(t.sessCtx, t.client, t.database, input)
}

func (t *MongoTransaction) ReplaceOne(input MongoReplaceInput) error {
	return mongoReplaceOneWith(t.sessCtx, t.client, t.database, input)
}

// Lock/Unlock pin one operation at a time to the transaction, the same
// serialization the SQL Transaction type gets from its own connection.
func (t *MongoTransaction) Lock()   { t.mu.Lock() }
func (t *MongoTransaction) Unlock() { t.mu.Unlock() }

func (t *MongoTransaction) close() {
	t.finish.Do(func() {
		t.finished = true
		t.session.EndSession(context.Background())
		t.cancel()
		t.cleanup()
	})
}

func (t *MongoTransaction) Commit() error {
	if t.finished {
		return errors.New("transaction already finished")
	}
	err := t.session.CommitTransaction(context.Background())
	t.close()
	return err
}

func (t *MongoTransaction) Rollback() error {
	if t.finished {
		return nil
	}
	err := t.session.AbortTransaction(context.Background())
	t.close()
	return err
}

type MongoAggregateInput struct {
	Database   string          `json:"database"`
	Collection string          `json:"collection"`
	Pipeline   json.RawMessage `json:"pipeline"`
	MaxTimeMs  int64           `json:"maxTimeMs"`
	Limit      int             `json:"limit"`
}

// writeAggregationStages rejects stages that write ($out, $merge), run
// server-side JavaScript ($function, $accumulator, $where) or read another
// connection's data ($lookup, $graphLookup, $unionWith, $currentOp,
// $listSessions, $planCacheStats). Aggregation here is read-only and scoped
// to the one collection given.
var forbiddenAggregationStages = map[string]bool{
	"$out": true, "$merge": true, "$function": true, "$accumulator": true, "$where": true,
	"$lookup": true, "$graphLookup": true, "$unionWith": true,
	"$currentOp": true, "$listSessions": true, "$listLocalSessions": true, "$planCacheStats": true,
}

func ValidateMongoPipeline(raw json.RawMessage) (bson.A, error) {
	var pipeline bson.A
	if err := bson.UnmarshalExtJSON(raw, false, &pipeline); err != nil {
		return nil, fmt.Errorf("pipeline must be an Extended JSON array of stage objects: %w", err)
	}
	if len(pipeline) == 0 {
		return nil, errors.New("pipeline must have at least one stage")
	}
	for _, stage := range pipeline {
		doc, ok := stage.(bson.D)
		if !ok {
			return nil, errors.New("each pipeline stage must be an object")
		}
		for _, entry := range doc {
			if forbiddenAggregationStages[entry.Key] {
				return nil, fmt.Errorf("stage %s is not supported; aggregation here is read-only", entry.Key)
			}
		}
	}
	return pipeline, nil
}

// MongoAggregate runs a read-only aggregation pipeline against one
// collection. $out/$merge, server-side JavaScript and cross-collection
// stages are rejected before anything runs.
func (m *Manager) MongoAggregate(ctx context.Context, connection Connection, input MongoAggregateInput) ([]json.RawMessage, bool, error) {
	if strings.TrimSpace(input.Collection) == "" || strings.ContainsRune(input.Collection, 0) {
		return nil, false, errors.New("collection is required")
	}
	pipeline, err := ValidateMongoPipeline(input.Pipeline)
	if err != nil {
		return nil, false, err
	}
	if input.MaxTimeMs < 0 || input.MaxTimeMs > 600000 {
		return nil, false, errors.New("maxTimeMs must be between 0 and 600000")
	}
	if input.Limit < 1 || input.Limit > 10000 {
		return nil, false, errors.New("limit must be between 1 and 10000")
	}
	client, cleanup, err := mongoClient(ctx, connection)
	if err != nil {
		return nil, false, err
	}
	defer cleanup()
	opts := options.Aggregate()
	if input.MaxTimeMs > 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, time.Duration(input.MaxTimeMs)*time.Millisecond)
		defer timeoutCancel()
	}
	cursor, err := client.Database(connection.Database).Collection(input.Collection).Aggregate(ctx, pipeline, opts)
	if err != nil {
		return nil, false, err
	}
	defer cursor.Close(ctx)
	docs := []json.RawMessage{}
	for cursor.Next(ctx) {
		if len(docs) == input.Limit {
			return docs, true, nil
		}
		raw, err := bson.MarshalExtJSON(cursor.Current, true, false)
		if err != nil {
			return nil, false, err
		}
		docs = append(docs, json.RawMessage(raw))
	}
	return docs, false, cursor.Err()
}

func (m *Manager) MongoFind(ctx context.Context, connection Connection, input MongoFindInput) ([]json.RawMessage, bool, error) {
	client, cleanup, err := mongoClient(ctx, connection)
	if err != nil {
		return nil, false, err
	}
	defer cleanup()
	return mongoFindWith(ctx, client, connection.Database, input)
}

func mongoFindWith(ctx context.Context, client *mongo.Client, database string, input MongoFindInput) ([]json.RawMessage, bool, error) {
	if strings.TrimSpace(input.Collection) == "" || strings.ContainsRune(input.Collection, 0) {
		return nil, false, errors.New("collection is required")
	}
	filter, err := ParseMongoFilter(input.Filter)
	if err != nil {
		return nil, false, err
	}
	var sort bson.D
	if len(input.Sort) > 0 {
		if err = bson.UnmarshalExtJSON(input.Sort, false, &sort); err != nil {
			return nil, false, errors.New("sort must be an object of field names and 1 or -1")
		}
	}
	for _, e := range sort {
		if fmt.Sprint(e.Value) != "1" && fmt.Sprint(e.Value) != "-1" {
			return nil, false, errors.New("sort directions must be 1 or -1")
		}
	}
	var project bson.D
	if len(input.Project) > 0 {
		if err = bson.UnmarshalExtJSON(input.Project, false, &project); err != nil {
			return nil, false, errors.New("project must be an object of field names and 0 or 1")
		}
	}
	if input.Skip < 0 {
		return nil, false, errors.New("skip must be zero or a positive integer")
	}
	if input.MaxTimeMs < 0 || input.MaxTimeMs > 600000 {
		return nil, false, errors.New("maxTimeMs must be between 0 and 600000")
	}
	if input.Limit < 1 || input.Limit > 10000 {
		return nil, false, errors.New("limit must be between 1 and 10000")
	}
	opts := options.Find().SetLimit(int64(input.Limit + 1))
	if len(sort) > 0 {
		opts.SetSort(sort)
	}
	if len(project) > 0 {
		opts.SetProjection(project)
	}
	if input.Skip > 0 {
		opts.SetSkip(int64(input.Skip))
	}
	if input.MaxTimeMs > 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, time.Duration(input.MaxTimeMs)*time.Millisecond)
		defer timeoutCancel()
	}
	cursor, err := client.Database(database).Collection(input.Collection).Find(ctx, filter, opts)
	if err != nil {
		return nil, false, err
	}
	defer cursor.Close(ctx)
	docs := []json.RawMessage{}
	for cursor.Next(ctx) {
		if len(docs) == input.Limit {
			return docs, true, nil
		}
		raw, err := bson.MarshalExtJSON(cursor.Current, true, false)
		if err != nil {
			return nil, false, err
		}
		docs = append(docs, json.RawMessage(raw))
	}
	return docs, false, cursor.Err()
}
