package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
)

// streamColumnOrigins reports the table column behind each result column, or
// null where the database did not say (expressions, joins it cannot trace).
func streamColumnOrigins(stream queryRowStream) []any {
	typed, ok := stream.(interface{ ColumnOrigins() []domain.ColumnOrigin })
	if !ok {
		return nil
	}
	origins := typed.ColumnOrigins()
	out := make([]any, len(origins))
	for index, origin := range origins {
		if origin.Resolved && origin.Table != "" && origin.Column != "" {
			out[index] = map[string]string{"schema": origin.Schema, "table": origin.Table, "column": origin.Column}
		}
	}
	return out
}

func streamColumnTypes(stream queryRowStream) []string {
	if typed, ok := stream.(interface{ DatabaseTypes() []string }); ok {
		return typed.DatabaseTypes()
	}
	return []string{}
}

func (s *Server) streamNDJSON(w http.ResponseWriter, r *http.Request, connection domain.Connection, sql, normalized, hash, reference string, stream queryRowStream, transforms ResultTransforms, annotations Annotations, limited bool, limit int) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	encoder := json.NewEncoder(w)
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	writeErr := encoder.Encode(annotations.addTo(map[string]any{"type": "columns", "columns": stream.Columns(), "columnTypes": streamColumnTypes(stream), "columnOrigins": streamColumnOrigins(stream)}))
	flush()
	var streamErr error
	count := int64(0)
	truncated := false
	batch := make([][]any, 0, 128)
	// Rows with large values would make one huge NDJSON line, which a client
	// has to hold in memory before it can parse it, so batches are also
	// flushed by size.
	batchBytes, last := 0, time.Now()
	for writeErr == nil {
		row, ok, err := stream.Next()
		if err != nil {
			streamErr = err
			break
		}
		if !ok {
			break
		}
		if limited && limit > 0 && count >= int64(limit) {
			truncated = true
			break
		}
		transforms.apply(row)
		batch = append(batch, browserRow(row))
		batchBytes += rowBytes(row)
		count++
		if len(batch) >= 128 || batchBytes >= 512<<10 || time.Since(last) > 100*time.Millisecond {
			writeErr = encoder.Encode(map[string]any{"type": "rows", "rows": batch})
			flush()
			batch = make([][]any, 0, 128)
			batchBytes, last = 0, time.Now()
		}
	}
	if writeErr == nil && len(batch) > 0 {
		writeErr = encoder.Encode(map[string]any{"type": "rows", "rows": batch})
	}
	status, message := "success", ""
	if truncated {
		status = "truncated"
	}
	if streamErr != nil {
		status, message = "error", streamErr.Error()
	}
	if writeErr != nil {
		status, message = "error", writeErr.Error()
	}
	if writeErr == nil {
		tail := map[string]any{"type": "complete", "rowCount": count, "durationMs": stream.DurationMS(), "truncated": truncated}
		if truncated {
			tail["policyNotice"] = limitNotice(annotations, limit)
		}
		if streamErr != nil {
			tail["error"] = streamErr.Error()
		}
		if err := encoder.Encode(tail); err != nil {
			status, message = "error", err.Error()
		}
		flush()
	}
	s.recordActivity(r, connection.ID, sql, status, count, stream.DurationMS(), normalized, hash, auditMeta{decision: "allow", reference: reference, errorMessage: message})
}

// limitNotice says why a result stopped early: the editor showed the rows it
// asked for, or a policy capped the result.
func limitNotice(annotations Annotations, limit int) string {
	if annotations["limitedBy"] == "request" {
		return fmt.Sprintf("Showing the first %d rows this client asked for", limit)
	}
	return fmt.Sprintf("Result limited by policy to %d rows", limit)
}

// rowBytes estimates a row's size in the stream, to keep batches small
// enough for a browser to parse comfortably.
func rowBytes(row []any) int {
	size := 16
	for _, value := range row {
		switch v := value.(type) {
		case string:
			size += len(v) + 3
		case []byte:
			size += len(v)*2 + 5
		default:
			size += 12
		}
	}
	return size
}
