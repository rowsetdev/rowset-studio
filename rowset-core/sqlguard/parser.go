// Package sqlguard provides Rowset's dialect-aware SQL security analysis.
// It deliberately models only the structure needed for policy enforcement,
// statement rewriting, result-lineage fallback, and query fingerprinting.
package sqlguard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

type Kind string

// Dialect identifies the SQL surface accepted by the target engine. The
// security IR intentionally stays common; dialect-specific validation and
// rewriting can evolve behind this boundary without leaking vendor ASTs into
// Rowset Core.
type Dialect string

const (
	DialectGeneric  Dialect = "generic"
	DialectPostgres Dialect = "postgres"
	DialectMySQL    Dialect = "mysql"
	DialectMSSQL    Dialect = "mssql"
)

const (
	Select  Kind = "select"
	Insert  Kind = "insert"
	Update  Kind = "update"
	Delete  Kind = "delete"
	DDL     Kind = "ddl"
	Session Kind = "session"
	Other   Kind = "other"
	Unknown Kind = "unknown"
	Multi   Kind = "multi"
)

type TableRef struct {
	Schema string
	Name   string
	Alias  string
}

type Info struct {
	Dialect Dialect
	Kind    Kind
	// Command is the outer statement that determines whether the database
	// returns rows. Kind remains the highest-risk operation found anywhere in
	// a CTE, so a data-changing CTE cannot masquerade as a read.
	Command    Kind
	Tables     []TableRef
	HasWhere   bool
	IsDrop     bool
	IsTruncate bool
	Raw        string
	Tokens     []Token
}

type Token struct {
	Text       string
	Lower      string
	Start, End int
	Depth      int
	Identifier bool
	// Quoted marks a quoted identifier ("where", `where`, [where]): its Lower
	// is the bare name, so it must never be read as a keyword.
	Quoted bool
}

// keyword reports whether the token is the unquoted keyword word.
func (t Token) keyword(word string) bool { return !t.Quoted && t.Lower == word }

// reservedWord reports whether the token is an unquoted clause keyword.
func (t Token) reservedWord() bool { return !t.Quoted && reserved[t.Lower] }

func Parse(sql string) (Info, error) {
	return ParseDialect(DialectGeneric, sql)
}

const (
	parseCacheShards     = 16
	parseCacheShardLimit = 512
	maxCachedSQLBytes    = 16 << 10
)

type parseCacheKey struct {
	dialect Dialect
	sql     string
}

type parseCacheShard struct {
	mu      sync.RWMutex
	entries map[parseCacheKey]Info
	order   []parseCacheKey
	next    int
}

var parseCache [parseCacheShards]parseCacheShard

// ParseDialect returns Rowset's compact security IR for one statement. It is
// deliberately not a general-purpose vendor AST. Successful parses are kept
// in a bounded, sharded cache; Info and its Tokens must be treated as immutable.
func ParseDialect(dialect Dialect, sql string) (Info, error) {
	if !dialect.Valid() {
		return Info{Dialect: dialect, Kind: Unknown, Raw: sql}, fmt.Errorf("unsupported SQL dialect %q", dialect)
	}
	key := parseCacheKey{dialect: dialect, sql: sql}
	if len(sql) <= maxCachedSQLBytes {
		shard := &parseCache[parseCacheIndex(key)]
		shard.mu.RLock()
		info, ok := shard.entries[key]
		shard.mu.RUnlock()
		if ok {
			return info, nil
		}
	}
	info, err := parseDialect(dialect, sql)
	if err == nil && len(sql) <= maxCachedSQLBytes {
		cacheParsed(key, info)
	}
	return info, err
}

func parseDialect(dialect Dialect, sql string) (Info, error) {
	statements, err := splitStatements(dialect, sql)
	if err != nil {
		return Info{Dialect: dialect, Kind: Unknown, Raw: sql}, err
	}
	if len(statements) == 0 {
		return Info{Dialect: dialect, Kind: Unknown, Raw: sql}, fmt.Errorf("empty SQL")
	}
	if len(statements) != 1 {
		return Info{Dialect: dialect, Kind: Multi, Raw: sql}, nil
	}
	raw := strings.TrimSpace(statements[0])
	tokens, err := LexDialect(dialect, raw)
	if err != nil || len(tokens) == 0 {
		return Info{Dialect: dialect, Kind: Unknown, Raw: raw}, fmt.Errorf("unclassifiable SQL: %w", err)
	}
	info := Info{Dialect: dialect, Raw: raw, Tokens: tokens}
	first := tokens[0].Lower
	switch first {
	case "select", "values", "table", "show", "explain", "describe", "desc", "pragma", "fetch":
		// FETCH reads the next rows of an open cursor, so it is a read like
		// any other: row limits and masking apply to what it returns.
		info.Kind = Select
	case "with":
		info.Kind, info.Command = classifyWith(tokens)
	case "insert", "replace", "upsert":
		info.Kind = Insert
	case "update", "merge":
		info.Kind = Update
	case "delete":
		info.Kind = Delete
	case "drop":
		info.Kind, info.IsDrop = DDL, true
	case "truncate":
		info.Kind, info.IsTruncate = DDL, true
	case "set", "use", "begin", "start", "commit", "rollback", "savepoint", "release", "discard",
		// Local to the session and changing no data: a variable, table
		// variable or cursor declaration, a message, and the rest of the
		// cursor verbs. Scripts that gather diagnostics start with these.
		"declare", "print", "open", "close", "deallocate":
		info.Kind = Session
	case "create", "alter", "rename", "comment", "grant", "revoke", "call", "copy", "execute", "exec", "vacuum", "analyze", "attach", "detach",
		"do", "load", "bulk", "optimize", "system", "kill", "put", "get", "remove", "undrop", "refresh", "reindex", "cluster", "dbcc":
		info.Kind = DDL
	default:
		info.Kind = Other
	}
	if info.Command == "" {
		info.Command = info.Kind
	}
	for _, token := range tokens {
		if token.Depth == 0 && token.keyword("where") {
			info.HasWhere = true
			break
		}
	}
	if info.Kind == Update || info.Kind == Delete {
		info.HasWhere = writesHaveEffectiveWhere(tokens)
	} else if info.HasWhere && info.Kind == Select && whereIsTautology(tokens) {
		info.HasWhere = false
	}
	info.Tables = extractTables(tokens)
	if writesOnlyToATableVariable(info) {
		info.Kind = Session
	}
	return info, nil
}

// writesOnlyToATableVariable reports a write whose target is a T-SQL table
// variable and which reads no table: it changes nothing in the database, so
// it is session state like any other local variable. A write that reads a
// table (INSERT INTO @rows SELECT ... FROM customers) keeps its kind, so
// masking and row filters still apply to what it reads.
func writesOnlyToATableVariable(info Info) bool {
	if len(info.Tables) > 0 || (info.Kind != Insert && info.Kind != Update && info.Kind != Delete) {
		return false
	}
	target := ""
	for index, token := range info.Tokens {
		if index+1 >= len(info.Tokens) {
			break
		}
		if token.Lower == "into" || token.Lower == "update" || (token.Lower == "from" && info.Kind == Delete) {
			target = info.Tokens[index+1].Text
			break
		}
	}
	return strings.HasPrefix(target, "@")
}

func parseCacheIndex(key parseCacheKey) uint32 {
	// FNV-1a without an allocation for dialect + SQL concatenation.
	hash := uint32(2166136261)
	for index := 0; index < len(key.dialect); index++ {
		hash = (hash ^ uint32(key.dialect[index])) * 16777619
	}
	for index := 0; index < len(key.sql); index++ {
		hash = (hash ^ uint32(key.sql[index])) * 16777619
	}
	return hash & (parseCacheShards - 1)
}

func cacheParsed(key parseCacheKey, info Info) {
	shard := &parseCache[parseCacheIndex(key)]
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if shard.entries == nil {
		shard.entries = make(map[parseCacheKey]Info, parseCacheShardLimit)
	}
	if _, exists := shard.entries[key]; exists {
		return
	}
	if len(shard.order) < parseCacheShardLimit {
		shard.order = append(shard.order, key)
	} else {
		delete(shard.entries, shard.order[shard.next])
		shard.order[shard.next] = key
		shard.next = (shard.next + 1) % parseCacheShardLimit
	}
	shard.entries[key] = info
}

// DialectForEngine maps a connection engine to the dialect whose lexing
// rules it follows. MariaDB shares MySQL's rules, including "#" comments.
func DialectForEngine(engine string) Dialect {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "postgres", "postgresql", "cockroach", "cockroachdb":
		return DialectPostgres
	case "mysql", "mariadb":
		return DialectMySQL
	case "mssql", "sqlserver":
		return DialectMSSQL
	default:
		return DialectGeneric
	}
}

// hashStartsComment reports whether "#" begins a line comment in dialect. It
// does on MySQL and MariaDB; on PostgreSQL "#" is an operator and on SQL
// Server it belongs to temporary-table names, so treating it as a comment
// there would hide a trailing ";" and the statement after it. Generic keeps
// the historical behaviour for callers with no known engine.
func hashStartsComment(dialect Dialect) bool {
	return dialect == DialectMySQL || dialect == DialectGeneric
}

func (dialect Dialect) Valid() bool {
	switch dialect {
	case DialectGeneric, DialectPostgres, DialectMySQL, DialectMSSQL:
		return true
	default:
		return false
	}
}

func whereIsTautology(tokens []Token) bool {
	start := -1
	var expression []Token
	for index, token := range tokens {
		if token.keyword("where") && token.Depth == 0 && start < 0 {
			start = index + 1
			continue
		}
		if start >= 0 && token.Depth == 0 && token.reservedWord() && token.Lower != "where" {
			break
		}
		if start >= 0 && index >= start {
			expression = append(expression, token)
		}
	}
	known, value := constantBoolean(expression)
	return known && value
}

func constantBoolean(tokens []Token) (bool, bool) {
	for wrapsAll(tokens) {
		tokens = tokens[1 : len(tokens)-1]
	}
	if len(tokens) == 0 {
		return false, false
	}
	depth := tokens[0].Depth
	for _, operator := range []string{"or", "and"} {
		found, allKnown, result := false, true, operator == "and"
		start := 0
		for index := 0; index <= len(tokens); index++ {
			if index < len(tokens) && (tokens[index].Depth != depth || !tokens[index].keyword(operator)) {
				continue
			}
			if index == len(tokens) && !found {
				break
			}
			found = true
			known, value := constantBoolean(tokens[start:index])
			if !known {
				allKnown = false
			} else if operator == "or" && value {
				return true, true
			} else if operator == "and" && !value {
				return true, false
			}
			result = result && value || operator == "or" && (result || value)
			start = index + 1
		}
		if found && allKnown {
			return true, result
		}
		if found {
			return false, false
		}
	}
	if len(tokens) == 1 && !tokens[0].Quoted {
		switch tokens[0].Lower {
		case "true":
			return true, true
		case "false":
			return true, false
		}
	}
	if len(tokens) >= 2 && tokens[0].keyword("not") {
		known, value := constantBoolean(tokens[1:])
		return known, !value
	}
	if len(tokens) == 3 {
		left, right, operator := tokens[0].Text, tokens[2].Text, tokens[1].Text
		switch operator {
		case "=":
			// Equal only when it holds on every row: the same column on both
			// sides, or two literals. A column against a literal is unknown.
			if strings.EqualFold(left, right) {
				return true, true
			}
			if literalToken(left) && literalToken(right) {
				return true, false
			}
			return false, false
		case "!", "<": // lexer splits != and <> into two tokens; handled below.
		}
		leftNumber, leftErr := strconv.ParseFloat(left, 64)
		rightNumber, rightErr := strconv.ParseFloat(right, 64)
		if leftErr == nil && rightErr == nil {
			switch operator {
			case ">":
				return true, leftNumber > rightNumber
			case "<":
				return true, leftNumber < rightNumber
			}
		}
	}
	// The lexer splits two-character operators into two tokens.
	if len(tokens) == 4 {
		left, right, operator := tokens[0].Text, tokens[3].Text, tokens[1].Text+tokens[2].Text
		switch operator {
		case "!=", "<>":
			// A column differs from a literal only on some rows.
			if strings.EqualFold(left, right) {
				return true, false
			}
			if literalToken(left) && literalToken(right) {
				return true, true
			}
		case "<=", ">=":
			leftNumber, leftErr := strconv.ParseFloat(left, 64)
			rightNumber, rightErr := strconv.ParseFloat(right, 64)
			if leftErr == nil && rightErr == nil {
				return true, operator == "<=" && leftNumber <= rightNumber || operator == ">=" && leftNumber >= rightNumber
			}
		}
	}
	return false, false
}

func literalToken(text string) bool {
	if _, err := strconv.ParseFloat(text, 64); err == nil {
		return true
	}
	return strings.HasPrefix(text, "'") || strings.HasPrefix(strings.ToUpper(text), "N'")
}

func wrapsAll(tokens []Token) bool {
	if len(tokens) < 2 || tokens[0].Text != "(" || tokens[len(tokens)-1].Text != ")" || tokens[0].Depth != tokens[len(tokens)-1].Depth {
		return false
	}
	base := tokens[0].Depth
	for _, token := range tokens[1 : len(tokens)-1] {
		if token.Depth <= base {
			return false
		}
	}
	return true
}

func classifyWith(tokens []Token) (Kind, Kind) {
	// Command is the first top-level operation after the CTE declarations.
	// Kind is promoted when a nested CTE itself changes data. UPDATE in a
	// SELECT ... FOR UPDATE clause is not a data-changing command token.
	command, risk := Select, Select
	for index, token := range tokens[1:] {
		actual := index + 1
		if actual > 0 && tokens[actual-1].Lower == "for" {
			continue
		}
		kind, operation := tokenKind(token.Lower)
		if !operation {
			continue
		}
		if token.Depth == 0 {
			command = kind
		}
		if kind == Insert || kind == Update || kind == Delete {
			risk = kind
		}
	}
	if risk == Select {
		risk = command
	}
	return risk, command
}

func tokenKind(value string) (Kind, bool) {
	switch value {
	case "select", "values", "table":
		return Select, true
	case "insert", "replace", "upsert":
		return Insert, true
	case "update", "merge":
		return Update, true
	case "delete":
		return Delete, true
	}
	return Other, false
}

func writesHaveEffectiveWhere(tokens []Token) bool {
	found := false
	for index, token := range tokens {
		if !token.keyword("update") && !token.keyword("merge") && !token.keyword("delete") || index > 0 && tokens[index-1].keyword("for") {
			continue
		}
		found = true
		where := -1
		for next := index + 1; next < len(tokens); next++ {
			candidate := tokens[next]
			if candidate.Depth < token.Depth {
				break
			}
			if candidate.Depth == token.Depth && candidate.keyword("where") {
				where = next
				break
			}
		}
		if where < 0 || whereAtDepthIsTautology(tokens[where+1:], token.Depth) {
			return false
		}
	}
	return found
}

func whereAtDepthIsTautology(tokens []Token, depth int) bool {
	expression := make([]Token, 0, len(tokens))
	for _, token := range tokens {
		if token.Depth < depth || token.Depth == depth && token.reservedWord() {
			break
		}
		expression = append(expression, token)
	}
	known, value := constantBoolean(expression)
	return known && value
}

func Normalize(info Info) (string, string) {
	parts := make([]string, 0, len(info.Tokens))
	for _, token := range info.Tokens {
		parts = append(parts, token.Lower)
	}
	normalized := strings.Join(parts, " ")
	sum := sha256.Sum256([]byte(normalized))
	return normalized, hex.EncodeToString(sum[:])
}

func ExactHash(sql string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sql)))
	return hex.EncodeToString(sum[:])
}

func IsWrite(kind Kind) bool {
	return kind == Insert || kind == Update || kind == Delete || kind == DDL
}

func SecondarySafe(info Info) bool {
	if info.Kind != Select {
		return false
	}
	dangerous := map[string]bool{"into": true, "insert": true, "update": true, "delete": true, "merge": true, "lock": true, "nextval": true, "setval": true, "pg_advisory_lock": true, "get_lock": true, "release_lock": true, "openrowset": true, "opendatasource": true}
	for index, token := range info.Tokens {
		if dangerous[token.Lower] {
			return false
		}
		if token.Lower == "for" && index+1 < len(info.Tokens) && (info.Tokens[index+1].Lower == "update" || info.Tokens[index+1].Lower == "share") {
			return false
		}
	}
	return true
}

func splitStatements(dialect Dialect, sql string) ([]string, error) {
	tokens, err := LexDialect(dialect, sql)
	if err != nil {
		return nil, err
	}
	var result []string
	start := 0
	// A procedure, function, trigger or event definition keeps the
	// semicolons of its body: BEGIN and CASE open a block and END closes it,
	// while END IF, END LOOP and the like close their own statements and
	// BEGIN TRAN opens no block.
	routine, depth, first := false, 0, true
	for index, token := range tokens {
		if first {
			routine, first = routineDefinition(tokens[index:]), false
		}
		if routine && token.Depth == 0 {
			switch token.Lower {
			case "case":
				depth++
			case "begin":
				if next := nextLower(tokens, index); next != "tran" && next != "transaction" && next != "distributed" && next != "work" {
					depth++
				}
			case "end":
				if next := nextLower(tokens, index); depth > 0 && next != "if" && next != "loop" && next != "while" && next != "repeat" {
					depth--
				}
			}
		}
		if token.Text == ";" && token.Depth == 0 && (!routine || depth == 0) {
			if part := strings.TrimSpace(sql[start:token.Start]); part != "" {
				result = append(result, part)
			}
			start = token.End
			first, depth = true, 0
		}
	}
	if part := strings.TrimSpace(sql[start:]); part != "" {
		result = append(result, part)
	}
	return result, nil
}

func nextLower(tokens []Token, index int) string {
	if index+1 < len(tokens) {
		return tokens[index+1].Lower
	}
	return ""
}

// routineDefinition reports whether tokens start CREATE [OR REPLACE | OR
// ALTER] [DEFINER = ...] PROCEDURE, FUNCTION, TRIGGER or EVENT.
func routineDefinition(tokens []Token) bool {
	if len(tokens) == 0 || tokens[0].Lower != "create" {
		return false
	}
	for _, token := range tokens[1:min(len(tokens), 16)] {
		if token.Depth != 0 {
			return false
		}
		switch token.Lower {
		case "procedure", "proc", "function", "trigger", "event":
			return true
		case "table", "view", "index", "unique", "schema", "database", "sequence", "type", "user", "role", "materialized", "temporary", "temp", "extension", "as", "select":
			return false
		}
	}
	return false
}

// Lex tokenizes with the generic dialect. Use LexDialect when the engine is
// known so "#" is a comment only where it should be.
func Lex(sql string) ([]Token, error) {
	return LexDialect(DialectGeneric, sql)
}

// LexDialect tokenizes sql using dialect's comment rules.
func LexDialect(dialect Dialect, sql string) ([]Token, error) {
	var out []Token
	depth := 0
	for i := 0; i < len(sql); {
		if unicode.IsSpace(rune(sql[i])) {
			i++
			continue
		}
		if i+1 < len(sql) && sql[i:i+2] == "--" {
			i += 2
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			continue
		}
		if sql[i] == '#' && hashStartsComment(dialect) {
			i++
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(sql) && sql[i:i+2] == "/*" {
			commentDepth := 1
			i += 2
			for i < len(sql) && commentDepth > 0 {
				if i+1 < len(sql) && sql[i:i+2] == "/*" {
					commentDepth++
					i += 2
				} else if i+1 < len(sql) && sql[i:i+2] == "*/" {
					commentDepth--
					i += 2
				} else {
					i++
				}
			}
			if commentDepth != 0 {
				return nil, fmt.Errorf("unterminated comment")
			}
			continue
		}
		start := i
		if sql[i] == '$' {
			if delimiter, ok := dollarQuoteDelimiter(sql[i:]); ok {
				closing := strings.Index(sql[i+len(delimiter):], delimiter)
				if closing < 0 {
					return nil, fmt.Errorf("unterminated dollar-quoted string")
				}
				i += len(delimiter) + closing + len(delimiter)
				out = append(out, Token{Text: sql[start:i], Lower: "?", Start: start, End: i, Depth: depth})
				continue
			}
		}
		if sql[i] == '\'' {
			i++
			for i < len(sql) {
				if sql[i] == '\'' {
					if i+1 < len(sql) && sql[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			if i > len(sql) || sql[i-1] != '\'' {
				return nil, fmt.Errorf("unterminated string")
			}
			out = append(out, Token{Text: sql[start:i], Lower: "?", Start: start, End: i, Depth: depth})
			continue
		}
		if sql[i] == '"' || sql[i] == '`' || sql[i] == '[' {
			open := sql[i]
			close := open
			if open == '[' {
				close = ']'
			}
			i++
			for i < len(sql) {
				if sql[i] == close {
					if i+1 < len(sql) && sql[i+1] == close {
						i += 2
						continue
					}
					break
				}
				i++
			}
			if i >= len(sql) {
				return nil, fmt.Errorf("unterminated quoted identifier")
			}
			i++
			text := sql[start:i]
			out = append(out, Token{Text: text, Lower: strings.ToLower(strings.Trim(text, "\"`[]")), Start: start, End: i, Depth: depth, Identifier: true, Quoted: true})
			continue
		}
		if isIdent(sql[i]) {
			i++
			for i < len(sql) && isIdent(sql[i]) {
				i++
			}
			text := sql[start:i]
			out = append(out, Token{Text: text, Lower: strings.ToLower(text), Start: start, End: i, Depth: depth, Identifier: true})
			continue
		}
		switch sql[i] {
		case '(':
			out = append(out, Token{Text: "(", Lower: "(", Start: i, End: i + 1, Depth: depth})
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced parenthesis")
			}
			out = append(out, Token{Text: ")", Lower: ")", Start: i, End: i + 1, Depth: depth})
		default:
			out = append(out, Token{Text: sql[i : i+1], Lower: strings.ToLower(sql[i : i+1]), Start: i, End: i + 1, Depth: depth})
		}
		i++
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced parenthesis")
	}
	return out, nil
}

func dollarQuoteDelimiter(sql string) (string, bool) {
	if len(sql) < 2 || sql[0] != '$' {
		return "", false
	}
	for index := 1; index < len(sql); index++ {
		if sql[index] == '$' {
			return sql[:index+1], true
		}
		if !(sql[index] == '_' || sql[index] >= 'a' && sql[index] <= 'z' || sql[index] >= 'A' && sql[index] <= 'Z' || index > 1 && sql[index] >= '0' && sql[index] <= '9') {
			return "", false
		}
	}
	return "", false
}

func isIdent(ch byte) bool {
	return ch == '_' || ch == '$' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9'
}

func extractTables(tokens []Token) []TableRef {
	var tables []TableRef
	seen := map[string]bool{}
	for i := 0; i < len(tokens); i++ {
		keyword := tokens[i].Lower
		isTable := keyword == "from" || keyword == "join" || keyword == "update" || keyword == "into" || keyword == "truncate"
		if keyword == "delete" && i+1 < len(tokens) && tokens[i+1].Lower == "from" {
			continue
		}
		if !isTable {
			continue
		}
		j := i + 1
		if j < len(tokens) && tokens[j].Lower == "table" {
			j++
		}
		if j >= len(tokens) || tokens[j].Text == "(" || !tokens[j].Identifier {
			continue
		}
		parts := []string{tokens[j].Lower}
		j++
		for j+1 < len(tokens) && tokens[j].Text == "." && tokens[j+1].Identifier {
			parts = append(parts, tokens[j+1].Lower)
			j += 2
		}
		ref := TableRef{Name: parts[len(parts)-1]}
		if len(parts) > 1 {
			ref.Schema = parts[len(parts)-2]
		}
		if j < len(tokens) && tokens[j].Lower == "as" {
			j++
		}
		if j < len(tokens) && tokens[j].Identifier && !reserved[tokens[j].Lower] {
			ref.Alias = tokens[j].Lower
		}
		key := ref.Schema + "." + ref.Name + "." + ref.Alias
		if !seen[key] {
			seen[key] = true
			tables = append(tables, ref)
		}
	}
	return tables
}

var reserved = map[string]bool{"where": true, "join": true, "left": true, "right": true, "full": true, "inner": true, "outer": true, "cross": true, "on": true, "group": true, "order": true, "limit": true, "offset": true, "fetch": true, "returning": true, "union": true, "intersect": true, "except": true, "set": true, "values": true, "using": true, "with": true, "use": true, "force": true, "ignore": true, "window": true, "qualify": true, "for": true, "having": true}
