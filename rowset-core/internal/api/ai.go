package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

// Assistants are reached either through a provider's API with the user's own
// key, or through a command-line tool already signed in on this computer.
const (
	aiProviderNone      = "none"
	aiProviderAnthropic = "anthropic"
	aiProviderOpenAI    = "openai"
	aiProviderCLI       = "cli"
)

// aiCommands are the command-line assistants Rowset knows how to call. Each
// reads the prompt on standard input and answers on standard output.
var aiCommands = map[string][]string{
	"claude": {"-p"},
	"codex":  {"exec", "-"},
}

const aiSchemaTableLimit = 120

type aiSettingsInput struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	APIKey      string `json:"apiKey"`
	ShareSchema bool   `json:"shareSchema"`
}

type aiAskInput struct {
	ConnectionID string `json:"connectionId"`
	Database     string `json:"database"`
	// Task is write, explain, optimize, indexes or fix.
	Task     string `json:"task"`
	Question string `json:"question"`
	SQL      string `json:"sql"`
	Error    string `json:"error"`
	Plan     string `json:"plan"`
}

func (s *Server) aiSettings(w http.ResponseWriter, r *http.Request) {
	identity := identityFromContext(r.Context())
	settings, err := s.store.AISettings(r.Context(), identity.UserID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the assistant settings could not be read")
		return
	}
	if settings.Provider == "" {
		settings.Provider, settings.ShareSchema = aiProviderNone, true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider":    settings.Provider,
		"model":       settings.Model,
		"shareSchema": settings.ShareSchema,
		"hasKey":      len(settings.Ciphertext) > 0,
		"commands":    availableAICommands(),
	})
}

// availableAICommands lists the assistant tools installed on this computer.
func availableAICommands() []string {
	found := make([]string, 0, len(aiCommands))
	for name := range aiCommands {
		if _, err := exec.LookPath(name); err == nil {
			found = append(found, name)
		}
	}
	sort.Strings(found)
	return found
}

func (s *Server) saveAISettings(w http.ResponseWriter, r *http.Request) {
	var input aiSettingsInput
	if !decodeJSON(w, r, &input) {
		return
	}
	identity := identityFromContext(r.Context())
	input.Provider, input.Model, input.APIKey = strings.TrimSpace(input.Provider), strings.TrimSpace(input.Model), strings.TrimSpace(input.APIKey)
	switch input.Provider {
	case aiProviderNone:
		if err := s.store.DeleteAISettings(r.Context(), identity.UserID); err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL", "the assistant settings could not be saved")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"provider": aiProviderNone, "shareSchema": true, "hasKey": false, "commands": availableAICommands()})
		return
	case aiProviderAnthropic, aiProviderOpenAI, aiProviderCLI:
	default:
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "choose none, anthropic, openai or cli")
		return
	}
	if input.Provider == aiProviderCLI {
		if _, ok := aiCommands[input.Model]; !ok {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "choose one of the assistant commands this computer has")
			return
		}
		if _, err := exec.LookPath(input.Model); err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", input.Model+" is not installed on this computer")
			return
		}
	}
	existing, err := s.store.AISettings(r.Context(), identity.UserID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the assistant settings could not be read")
		return
	}
	settings := store.AISettings{UserID: identity.UserID, Provider: input.Provider, Model: input.Model, ShareSchema: input.ShareSchema, UpdatedAt: store.NowString()}
	settings.Ciphertext, settings.Nonce = existing.Ciphertext, existing.Nonce
	if input.APIKey != "" {
		if s.vault == nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL", "secret storage unavailable")
			return
		}
		if settings.Ciphertext, settings.Nonce, err = s.vault.Encrypt([]byte(input.APIKey)); err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL", "the key could not be stored")
			return
		}
	}
	if input.Provider != aiProviderCLI && len(settings.Ciphertext) == 0 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "this provider needs an API key")
		return
	}
	if err := s.store.SaveAISettings(r.Context(), settings); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the assistant settings could not be saved")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"provider": settings.Provider, "model": settings.Model, "shareSchema": settings.ShareSchema, "hasKey": len(settings.Ciphertext) > 0, "commands": availableAICommands()})
}

// askAI answers a question about the connected database. The statement, the
// error and — unless the user turned that off — the names and indexes of the
// tables are sent to the assistant; table contents never are.
func (s *Server) askAI(w http.ResponseWriter, r *http.Request) {
	defer s.holdAwake()()
	identity := identityFromContext(r.Context())
	settings, err := s.store.AISettings(r.Context(), identity.UserID)
	if err != nil || settings.Provider == "" || settings.Provider == aiProviderNone {
		writeError(w, http.StatusBadRequest, "AI_NOT_CONFIGURED", "set up the assistant in Account first")
		return
	}
	var input aiAskInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Task = strings.TrimSpace(strings.ToLower(input.Task))
	if !slicesContains([]string{"write", "explain", "optimize", "indexes", "fix"}, input.Task) {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "task must be write, explain, optimize, indexes or fix")
		return
	}
	schemaText := ""
	if settings.ShareSchema && strings.TrimSpace(input.ConnectionID) != "" {
		connection, err := s.store.Connection(r.Context(), strings.TrimSpace(input.ConnectionID))
		if err == nil && connection.OrgID == identity.OrgID {
			schemaText = s.schemaSummary(r, connection, strings.TrimSpace(input.Database))
		}
	}
	prompt := aiPrompt(input, schemaText)
	key := ""
	if len(settings.Ciphertext) > 0 && s.vault != nil {
		plain, err := s.vault.Decrypt(settings.Ciphertext, settings.Nonce)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL", "the stored key could not be read")
			return
		}
		key = string(plain)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	answer, err := askAssistant(ctx, settings, key, prompt)
	if err != nil {
		writeError(w, http.StatusBadGateway, "AI_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"answer": answer, "sql": firstSQLBlock(answer), "sharedSchema": schemaText != ""})
}

func slicesContains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

// schemaSummary lists tables, their columns and their indexes compactly
// enough to fit in a prompt.
func (s *Server) schemaSummary(r *http.Request, connection domain.Connection, database string) string {
	target, err := s.metadataEngineConnection(r.Context(), connection, database)
	if err != nil {
		return ""
	}
	ctx, cancel := withConnectionTimeout(r, connection, 0, 60*time.Second)
	defer cancel()
	schema, err := s.engines.CachedSchema(ctx, target, false)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(schema.Tables))
	for name := range schema.Tables {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > aiSchemaTableLimit {
		names = names[:aiSchemaTableLimit]
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Engine: %s\nDatabase: %s\n\n", connection.Engine, database)
	for _, name := range names {
		columns := schema.Tables[name]
		kind := "TABLE"
		if schema.Views[name] {
			kind = "VIEW"
		}
		fmt.Fprintf(&out, "%s %s\n", kind, name)
		for _, column := range columns {
			marks := ""
			if column.PrimaryKey {
				marks += " PK"
			}
			if column.References != "" {
				marks += " FK -> " + column.References
			}
			if !column.Nullable {
				marks += " NOT NULL"
			}
			fmt.Fprintf(&out, "  %s %s%s\n", column.Name, column.DataType, marks)
		}
		for _, index := range schema.Indexes[name] {
			unique := ""
			if index.Unique {
				unique = " UNIQUE"
			}
			fmt.Fprintf(&out, "  INDEX%s %s (%s)\n", unique, index.Name, strings.Join(index.Columns, ", "))
		}
		out.WriteString("\n")
	}
	return out.String()
}

// aiPrompt states the task, then the statement and the schema it concerns.
func aiPrompt(input aiAskInput, schema string) string {
	var out strings.Builder
	out.WriteString("You are a database assistant inside a SQL editor. Answer briefly and concretely.\n")
	switch input.Task {
	case "write":
		out.WriteString("Write one SQL statement that answers the request. Use only the tables and columns listed below. Put the statement in a ```sql block, then explain it in at most three sentences.\n")
	case "explain":
		out.WriteString("Explain what the statement does, in at most six sentences. Mention the tables it reads and anything that looks expensive.\n")
	case "optimize":
		out.WriteString("Rewrite the statement so it runs faster while returning the same rows, using the indexes that exist. Put the rewritten statement in a ```sql block and say what you changed and why. If it is already fine, say so instead of inventing changes.\n")
	case "indexes":
		out.WriteString("Say which indexes would make this statement faster. Judge the existing indexes first; only suggest a new one if it clearly helps, and give the CREATE INDEX statement in a ```sql block with the reason and the cost of keeping it.\n")
	case "fix":
		out.WriteString("The statement failed. Explain the cause in one or two sentences and give the corrected statement in a ```sql block.\n")
	}
	if question := strings.TrimSpace(input.Question); question != "" {
		out.WriteString("\nRequest:\n" + question + "\n")
	}
	if sql := strings.TrimSpace(input.SQL); sql != "" {
		out.WriteString("\nStatement:\n```sql\n" + sql + "\n```\n")
	}
	if failure := strings.TrimSpace(input.Error); failure != "" {
		out.WriteString("\nThe database reported:\n" + failure + "\n")
	}
	if plan := strings.TrimSpace(input.Plan); plan != "" {
		out.WriteString("\nExecution plan:\n" + plan + "\n")
	}
	if schema != "" {
		out.WriteString("\nSchema (names, types and indexes only; no data):\n" + schema)
	} else {
		out.WriteString("\nNo schema was shared, so do not guess table or column names that were not mentioned.\n")
	}
	return out.String()
}

// firstSQLBlock returns the statement from a ```sql block, so the editor can
// offer to insert it.
func firstSQLBlock(answer string) string {
	lower := strings.ToLower(answer)
	start := strings.Index(lower, "```sql")
	if start < 0 {
		return ""
	}
	rest := answer[start+len("```sql"):]
	if end := strings.Index(rest, "```"); end >= 0 {
		return strings.TrimSpace(rest[:end])
	}
	return strings.TrimSpace(rest)
}

func askAssistant(ctx context.Context, settings store.AISettings, key, prompt string) (string, error) {
	switch settings.Provider {
	case aiProviderCLI:
		return askCommand(ctx, settings.Model, prompt)
	case aiProviderAnthropic:
		model := settings.Model
		if model == "" {
			model = "claude-sonnet-5"
		}
		body := map[string]any{"model": model, "max_tokens": 1500, "messages": []map[string]string{{"role": "user", "content": prompt}}}
		return askHTTP(ctx, "https://api.anthropic.com/v1/messages", map[string]string{"x-api-key": key, "anthropic-version": "2023-06-01"}, body, anthropicText)
	default:
		model := settings.Model
		if model == "" {
			model = "gpt-4.1-mini"
		}
		body := map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": prompt}}}
		return askHTTP(ctx, "https://api.openai.com/v1/chat/completions", map[string]string{"Authorization": "Bearer " + key}, body, openAIText)
	}
}

// askCommand runs an assistant installed on this computer, which keeps the
// account and its credentials outside Rowset.
func askCommand(ctx context.Context, name, prompt string) (string, error) {
	arguments, ok := aiCommands[name]
	if !ok {
		return "", errors.New("unknown assistant command")
	}
	command := exec.CommandContext(ctx, name, arguments...)
	command.Stdin = strings.NewReader(prompt)
	var out, failure bytes.Buffer
	command.Stdout, command.Stderr = &out, &failure
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(failure.String())
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("%s: %s", name, message)
	}
	answer := strings.TrimSpace(out.String())
	if answer == "" {
		return "", fmt.Errorf("%s answered nothing", name)
	}
	return answer, nil
}

func askHTTP(ctx context.Context, url string, headers map[string]string, body map[string]any, read func([]byte) (string, error)) (string, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := (&http.Client{Timeout: 5 * time.Minute}).Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	raw := make([]byte, 0, 4096)
	buffer := make([]byte, 4096)
	for {
		read, err := response.Body.Read(buffer)
		raw = append(raw, buffer[:read]...)
		if err != nil || len(raw) > 1<<20 {
			break
		}
	}
	if response.StatusCode >= 300 {
		return "", fmt.Errorf("the assistant refused the request (%d): %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	return read(raw)
}

func anthropicText(raw []byte) (string, error) {
	var body struct {
		Content []struct {
			Type, Text string
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", err
	}
	var out strings.Builder
	for _, part := range body.Content {
		if part.Type == "text" {
			out.WriteString(part.Text)
		}
	}
	if out.Len() == 0 {
		return "", errors.New("the assistant answered nothing")
	}
	return out.String(), nil
}

func openAIText(raw []byte) (string, error) {
	var body struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", err
	}
	if len(body.Choices) == 0 || strings.TrimSpace(body.Choices[0].Message.Content) == "" {
		return "", errors.New("the assistant answered nothing")
	}
	return body.Choices[0].Message.Content, nil
}
