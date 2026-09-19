package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata" // time zones on systems without a zoneinfo database (Windows)

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/id"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

const scheduledSQLLimit = 256 * 1024

// scheduleSpec says when a scheduled query runs, in its own time zone.
type scheduleSpec struct {
	Kind         string `json:"kind"`                   // daily, weekly or interval
	Time         string `json:"time,omitempty"`         // HH:MM for daily and weekly
	Days         []int  `json:"days,omitempty"`         // weekly: 0 = Sunday … 6 = Saturday
	EveryMinutes int    `json:"everyMinutes,omitempty"` // interval
	Timezone     string `json:"timezone"`
}

func (spec scheduleSpec) clock() (int, int, error) {
	at, err := time.Parse("15:04", spec.Time)
	if err != nil {
		return 0, 0, errors.New("time must look like 09:30")
	}
	return at.Hour(), at.Minute(), nil
}

func (spec scheduleSpec) validate() error {
	if _, err := time.LoadLocation(spec.Timezone); err != nil || spec.Timezone == "" {
		return errors.New("choose a valid time zone")
	}
	switch spec.Kind {
	case "interval":
		if spec.EveryMinutes < 5 || spec.EveryMinutes > 7*24*60 {
			return errors.New("an interval must be between 5 minutes and 7 days")
		}
	case "daily", "weekly":
		if _, _, err := spec.clock(); err != nil {
			return err
		}
		if spec.Kind == "weekly" {
			if len(spec.Days) == 0 {
				return errors.New("choose at least one day")
			}
			for _, day := range spec.Days {
				if day < 0 || day > 6 {
					return errors.New("days must be 0 (Sunday) to 6 (Saturday)")
				}
			}
		}
	default:
		return errors.New("schedule kind must be daily, weekly or interval")
	}
	return nil
}

// next returns the first run strictly after the given time. Wall-clock times
// that do not exist because of a daylight-saving change move forward.
func (spec scheduleSpec) next(after time.Time) (time.Time, error) {
	if err := spec.validate(); err != nil {
		return time.Time{}, err
	}
	if spec.Kind == "interval" {
		return after.Add(time.Duration(spec.EveryMinutes) * time.Minute).Truncate(time.Second), nil
	}
	location, _ := time.LoadLocation(spec.Timezone)
	hour, minute, _ := spec.clock()
	local := after.In(location)
	for offset := 0; offset <= 8; offset++ {
		day := local.AddDate(0, 0, offset)
		candidate := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, location)
		if candidate.Hour() != hour || candidate.Minute() != minute {
			// The wall-clock time does not exist on this day (clocks sprang
			// forward); run at the same time on the new offset instead.
			candidate = candidate.Add(time.Hour)
		}
		if !candidate.After(after) {
			continue
		}
		if spec.Kind == "weekly" && !containsDay(spec.Days, int(candidate.Weekday())) {
			continue
		}
		return candidate, nil
	}
	return time.Time{}, errors.New("the schedule has no next run")
}

func containsDay(days []int, day int) bool {
	for _, item := range days {
		if item == day {
			return true
		}
	}
	return false
}

type scheduledInput struct {
	Name         string       `json:"name"`
	ConnectionID string       `json:"connectionId"`
	Database     string       `json:"database"`
	SQL          string       `json:"sql"`
	Schedule     scheduleSpec `json:"schedule"`
	OutputDir    string       `json:"outputDir"`
	Format       string       `json:"format"`
	CatchUp      bool         `json:"catchUp"`
	Enabled      bool         `json:"enabled"`
}

// The envelope binds the encrypted SQL to its query and owner.
type scheduledEnvelope struct {
	QueryID string `json:"queryId"`
	UserID  string `json:"userId"`
	SQL     string `json:"sql"`
}

func (s *Server) sealScheduledSQL(queryID, userID, sql string) ([]byte, []byte, error) {
	if s.vault == nil {
		return nil, nil, errors.New("secret vault unavailable")
	}
	plain, err := json.Marshal(scheduledEnvelope{QueryID: queryID, UserID: userID, SQL: sql})
	if err != nil {
		return nil, nil, err
	}
	return s.vault.Encrypt(plain)
}

func (s *Server) openScheduledSQL(item store.ScheduledQuery) (string, error) {
	if s.vault == nil {
		return "", errors.New("secret vault unavailable")
	}
	plain, err := s.vault.Decrypt(item.Ciphertext, item.Nonce)
	if err != nil {
		return "", err
	}
	var envelope scheduledEnvelope
	if err := json.Unmarshal(plain, &envelope); err != nil || envelope.QueryID != item.ID || envelope.UserID != item.UserID {
		return "", errors.New("scheduled query does not belong to this record")
	}
	return envelope.SQL, nil
}

// defaultOutputDir is where results go unless the owner picks a folder.
func defaultOutputDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Documents", "Rowset Studio")
}

// validateScheduled checks the input and returns the normalized query.
func (s *Server) validateScheduled(r *http.Request, identity domain.Identity, input *scheduledInput) (string, error) {
	input.Name, input.SQL = strings.TrimSpace(input.Name), strings.TrimSpace(input.SQL)
	input.Database, input.OutputDir = strings.TrimSpace(input.Database), strings.TrimSpace(input.OutputDir)
	input.Format = strings.ToLower(strings.TrimSpace(input.Format))
	if input.Name == "" || len(input.Name) > 120 {
		return "", errors.New("name is required (at most 120 characters)")
	}
	if input.SQL == "" || len(input.SQL) > scheduledSQLLimit {
		return "", errors.New("sql is required (at most 256 KB)")
	}
	info, err := sqlguard.Parse(input.SQL)
	if err != nil {
		return "", err
	}
	if info.Kind != sqlguard.Select {
		return "", errors.New("scheduled queries run one SELECT statement")
	}
	if err := input.Schedule.validate(); err != nil {
		return "", err
	}
	if input.OutputDir == "" {
		input.OutputDir = defaultOutputDir()
	}
	if !filepath.IsAbs(input.OutputDir) {
		return "", errors.New("the output folder must be a full path")
	}
	input.OutputDir = filepath.Clean(input.OutputDir)
	if input.Format == "" {
		input.Format = "csv"
	}
	if input.Format != "csv" && input.Format != "json" {
		return "", errors.New("format must be csv or json")
	}
	connection, err := s.store.Connection(r.Context(), input.ConnectionID)
	if err != nil {
		return "", errors.New("unknown connection")
	}
	if !engine.SupportsScheduledSQL(connection.Engine) {
		return "", errors.New("scheduled queries are not available for this engine")
	}
	if !s.canUseConnection(r.Context(), identity, connection) {
		return "", errors.New("unknown connection")
	}
	return input.SQL, nil
}

func nextRunString(spec scheduleSpec, enabled bool, now time.Time) (*string, error) {
	if !enabled {
		return nil, nil
	}
	next, err := spec.next(now)
	if err != nil {
		return nil, err
	}
	value := next.UTC().Format(time.RFC3339)
	return &value, nil
}

func (s *Server) scheduledJSON(item store.ScheduledQuery, last *store.ScheduledRun) map[string]any {
	sql, err := s.openScheduledSQL(item)
	if err != nil {
		sql = ""
	}
	var spec scheduleSpec
	_ = json.Unmarshal([]byte(item.Schedule), &spec)
	s.scheduleMu.Lock()
	running := s.scheduleRunning[item.ID]
	s.scheduleMu.Unlock()
	result := map[string]any{"id": item.ID, "name": item.Name, "connectionId": item.ConnectionID, "database": item.Database, "sql": sql, "schedule": spec, "outputDir": item.OutputDir, "format": item.OutputFormat, "catchUp": item.CatchUp, "enabled": item.Enabled, "nextRunAt": item.NextRunAt, "createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt, "running": running}
	if last != nil {
		result["lastRun"] = scheduledRunJSON(*last)
	}
	return result
}

func scheduledRunJSON(run store.ScheduledRun) map[string]any {
	return map[string]any{"id": run.ID, "trigger": run.Trigger, "startedAt": run.StartedAt, "finishedAt": run.FinishedAt, "status": run.Status, "rows": run.Rows, "outputPath": run.OutputPath, "error": run.Error}
}

func (s *Server) listScheduled(w http.ResponseWriter, r *http.Request) {
	identity := identityFromContext(r.Context())
	items, err := s.store.ListScheduledQueries(r.Context(), identity.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "scheduled queries unavailable")
		return
	}
	latest, err := s.store.LatestScheduledRuns(r.Context(), identity.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "scheduled runs could not be read")
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		var last *store.ScheduledRun
		if run, ok := latest[item.ID]; ok {
			last = &run
		}
		out = append(out, s.scheduledJSON(item, last))
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": out})
}

func (s *Server) scheduleDefaults(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"outputDir": defaultOutputDir()})
}

func (s *Server) createScheduled(w http.ResponseWriter, r *http.Request) {
	s.saveScheduled(w, r, nil)
}

func (s *Server) updateScheduled(w http.ResponseWriter, r *http.Request) {
	identity := identityFromContext(r.Context())
	existing, err := s.store.ScheduledQuery(r.Context(), r.PathValue("id"), identity.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "scheduled query not found")
		return
	}
	s.saveScheduled(w, r, &existing)
}

func (s *Server) saveScheduled(w http.ResponseWriter, r *http.Request, existing *store.ScheduledQuery) {
	var input scheduledInput
	if !decodeJSON(w, r, &input) {
		return
	}
	identity := identityFromContext(r.Context())
	sql, err := s.validateScheduled(r, identity, &input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	now := time.Now().UTC()
	next, err := nextRunString(input.Schedule, input.Enabled, now)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	spec, _ := json.Marshal(input.Schedule)
	item := store.ScheduledQuery{ID: id.New(), OrgID: identity.OrgID, UserID: identity.UserID, ConnectionID: input.ConnectionID, Database: input.Database, Name: input.Name, Schedule: string(spec), OutputDir: input.OutputDir, OutputFormat: input.Format, CatchUp: input.CatchUp, Enabled: input.Enabled, NextRunAt: next, CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339)}
	if existing != nil {
		item.ID, item.OrgID, item.CreatedAt = existing.ID, existing.OrgID, existing.CreatedAt
	}
	item.Ciphertext, item.Nonce, err = s.sealScheduledSQL(item.ID, item.UserID, sql)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "could not encrypt the query")
		return
	}
	status := http.StatusCreated
	if existing != nil {
		err, status = s.store.UpdateScheduledQuery(r.Context(), item), http.StatusOK
	} else {
		err = s.store.CreateScheduledQuery(r.Context(), item)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, status, s.scheduledJSON(item, nil))
}

func (s *Server) deleteScheduled(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteScheduledQuery(r.Context(), r.PathValue("id"), identityFromContext(r.Context()).UserID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) runScheduledNow(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.ScheduledQuery(r.Context(), r.PathValue("id"), identityFromContext(r.Context()).UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "scheduled query not found")
		return
	}
	if !s.startScheduled(item, "manual") {
		writeError(w, http.StatusConflict, "ALREADY_RUNNING", "this query is already running")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "started"})
}

func (s *Server) listScheduledRuns(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.ScheduledQuery(r.Context(), r.PathValue("id"), identityFromContext(r.Context()).UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "scheduled query not found")
		return
	}
	runs, err := s.store.ListScheduledRuns(r.Context(), item.ID, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", fmt.Sprintf("runs unavailable: %v", err))
		return
	}
	out := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		out = append(out, scheduledRunJSON(run))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}
