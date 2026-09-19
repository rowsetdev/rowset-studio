package sqlguard

import (
	"fmt"
	"strings"
)

// ParseCQL returns the policy IR for one CQL statement or one Cassandra batch.
// Cassandra batches contain semicolon-delimited child statements, so they
// cannot be treated as ordinary SQL multi-statements. Every child is parsed
// and the most restrictive policy attributes are folded into the result.
func ParseCQL(cql string) (Info, error) {
	raw := strings.TrimSpace(cql)
	tokens, err := LexDialect(DialectGeneric, raw)
	if err != nil || len(tokens) == 0 {
		return Info{Kind: Unknown, Raw: raw}, fmt.Errorf("unclassifiable CQL: %w", err)
	}
	if tokens[0].Lower != "begin" {
		return ParseDialect(DialectGeneric, raw)
	}

	batchToken := 1
	if batchToken < len(tokens) && (tokens[batchToken].Lower == "unlogged" || tokens[batchToken].Lower == "counter") {
		batchToken++
	}
	if batchToken >= len(tokens) || tokens[batchToken].Lower != "batch" {
		return ParseDialect(DialectGeneric, raw)
	}
	apply := -1
	for i := batchToken + 1; i+1 < len(tokens); i++ {
		if tokens[i].Lower == "apply" && tokens[i+1].Lower == "batch" {
			apply = i
			break
		}
	}
	if apply < 0 {
		return Info{Kind: Unknown, Raw: raw}, fmt.Errorf("CQL batch is missing APPLY BATCH")
	}
	bodyStart := tokens[batchToken].End
	bodyEnd := tokens[apply].Start
	children, err := splitStatements(DialectGeneric, raw[bodyStart:bodyEnd])
	if err != nil || len(children) == 0 {
		return Info{Kind: Unknown, Raw: raw}, fmt.Errorf("CQL batch has no statements")
	}
	result := Info{Kind: Insert, Command: Insert, Raw: raw, HasWhere: true}
	for _, child := range children {
		info, parseErr := ParseDialect(DialectGeneric, child)
		if parseErr != nil || (info.Kind != Insert && info.Kind != Update && info.Kind != Delete) {
			return Info{Kind: Unknown, Raw: raw}, fmt.Errorf("CQL batch contains an unsupported statement")
		}
		result.Tables = append(result.Tables, info.Tables...)
		if (info.Kind == Update || info.Kind == Delete) && !info.HasWhere {
			result.HasWhere = false
		}
		if info.Kind == Delete {
			result.Kind, result.Command = Delete, Delete
		} else if info.Kind == Update && result.Kind != Delete {
			result.Kind, result.Command = Update, Update
		}
	}
	return result, nil
}
