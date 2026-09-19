package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

// Notifications are posted to a Slack incoming webhook the user creates and
// pastes in; Rowset needs no Slack app of its own and no token.
const (
	slackRunsAll      = "all"
	slackRunsFailures = "failures"
	slackRunsOff      = "off"
)

type slackSettingsInput struct {
	WebhookURL       string `json:"webhookUrl"`
	ScheduleRuns     string `json:"scheduleRuns"`
	LongQuerySeconds int64  `json:"longQuerySeconds"`
}

func (s *Server) slackSettings(w http.ResponseWriter, r *http.Request) {
	identity := identityFromContext(r.Context())
	settings, err := s.store.SlackSettings(r.Context(), identity.UserID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the notification settings could not be read")
		return
	}
	if settings.ScheduleRuns == "" {
		settings.ScheduleRuns = slackRunsFailures
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured":       len(settings.Ciphertext) > 0,
		"scheduleRuns":     settings.ScheduleRuns,
		"longQuerySeconds": settings.LongQuerySeconds,
	})
}

func (s *Server) saveSlackSettings(w http.ResponseWriter, r *http.Request) {
	var input slackSettingsInput
	if !decodeJSON(w, r, &input) {
		return
	}
	identity := identityFromContext(r.Context())
	input.WebhookURL = strings.TrimSpace(input.WebhookURL)
	if input.WebhookURL == "" {
		if err := s.store.DeleteSlackSettings(r.Context(), identity.UserID); err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL", "the notification settings could not be saved")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "scheduleRuns": slackRunsFailures, "longQuerySeconds": 0})
		return
	}
	if !strings.HasPrefix(input.WebhookURL, "https://hooks.slack.com/") {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "paste the https://hooks.slack.com/… address Slack gave you")
		return
	}
	switch input.ScheduleRuns {
	case slackRunsAll, slackRunsFailures, slackRunsOff:
	default:
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "notify on all runs, failures only, or off")
		return
	}
	if input.LongQuerySeconds < 0 || input.LongQuerySeconds > 24*60*60 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "the long-statement threshold must be between 0 and 24 hours")
		return
	}
	if s.vault == nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "secret storage unavailable")
		return
	}
	ciphertext, nonce, err := s.vault.Encrypt([]byte(input.WebhookURL))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the address could not be stored")
		return
	}
	settings := store.SlackSettings{UserID: identity.UserID, Ciphertext: ciphertext, Nonce: nonce, ScheduleRuns: input.ScheduleRuns, LongQuerySeconds: input.LongQuerySeconds, UpdatedAt: store.NowString()}
	if err := s.store.SaveSlackSettings(r.Context(), settings); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the notification settings could not be saved")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "scheduleRuns": settings.ScheduleRuns, "longQuerySeconds": settings.LongQuerySeconds})
}

// testSlack posts one message, so the address can be checked while setting up.
func (s *Server) testSlack(w http.ResponseWriter, r *http.Request) {
	identity := identityFromContext(r.Context())
	url, err := s.slackWebhook(r.Context(), identity.UserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "SLACK_NOT_CONFIGURED", "add a webhook address first")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := postSlack(ctx, url, "Rowset is connected. Scheduled runs will be posted here."); err != nil {
		writeError(w, http.StatusBadGateway, "SLACK_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) slackWebhook(ctx context.Context, userID string) (string, error) {
	settings, err := s.store.SlackSettings(ctx, userID)
	if err != nil || len(settings.Ciphertext) == 0 || s.vault == nil {
		return "", errors.New("no webhook configured")
	}
	plain, err := s.vault.Decrypt(settings.Ciphertext, settings.Nonce)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// notifyScheduledRun posts the outcome of a scheduled query, if the user
// asked for it. The rows themselves are never sent; only what ran, how long
// it took and where the file went.
func (s *Server) notifyScheduledRun(userID, name string, rows int64, duration time.Duration, path string, failure error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	settings, err := s.store.SlackSettings(ctx, userID)
	if err != nil || len(settings.Ciphertext) == 0 {
		return
	}
	if settings.ScheduleRuns == slackRunsOff || (settings.ScheduleRuns == slackRunsFailures && failure == nil) {
		return
	}
	url, err := s.slackWebhook(ctx, userID)
	if err != nil {
		return
	}
	_ = postSlack(ctx, url, scheduledRunMessage(name, rows, duration, path, failure))
}

func scheduledRunMessage(name string, rows int64, duration time.Duration, path string, failure error) string {
	if failure != nil {
		return fmt.Sprintf(":x: Scheduled query *%s* failed after %s.\n%s", name, roundedDuration(duration), failure.Error())
	}
	message := fmt.Sprintf(":white_check_mark: Scheduled query *%s* wrote %d row(s) in %s.", name, rows, roundedDuration(duration))
	if path != "" {
		message += "\nFile: `" + path + "`"
	}
	return message
}

// notifyLongStatement posts when a statement in the editor took longer than
// the user's threshold, so a long report can be left running.
func (s *Server) notifyLongStatement(userID, connection, sql string, rows int64, duration time.Duration, failure string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	settings, err := s.store.SlackSettings(ctx, userID)
	if err != nil || len(settings.Ciphertext) == 0 || settings.LongQuerySeconds <= 0 || duration < time.Duration(settings.LongQuerySeconds)*time.Second {
		return
	}
	url, err := s.slackWebhook(ctx, userID)
	if err != nil {
		return
	}
	_ = postSlack(ctx, url, longStatementMessage(connection, sql, rows, duration, failure))
}

func longStatementMessage(connection, sql string, rows int64, duration time.Duration, failure string) string {
	headline := fmt.Sprintf(":hourglass_flowing_sand: Statement on *%s* finished in %s with %d row(s).", connection, roundedDuration(duration), rows)
	if failure != "" {
		headline = fmt.Sprintf(":x: Statement on *%s* failed after %s.\n%s", connection, roundedDuration(duration), failure)
	}
	return headline + "\n```" + firstLines(sql, 3) + "```"
}

func firstLines(text string, limit int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > limit {
		return strings.Join(lines[:limit], "\n") + "\n…"
	}
	return strings.Join(lines, "\n")
}

func roundedDuration(duration time.Duration) string {
	if duration >= time.Minute {
		return duration.Round(time.Second).String()
	}
	return duration.Round(time.Millisecond).String()
}

func postSlack(ctx context.Context, url, text string) error {
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return fmt.Errorf("Slack refused the message (%d); check the webhook address", response.StatusCode)
	}
	return nil
}
