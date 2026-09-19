package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/auth"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/id"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

func readEnvValues(path string) map[string]string {
	values := map[string]string{}
	file, err := os.Open(path)
	if err != nil {
		return values
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return values
}

func randomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func randomBase64(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(value), nil
}

// writePrivateConfig atomically writes an owner-only environment file.
func writePrivateConfig(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".rowset-env-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	_, err = io.WriteString(temporary, strings.Join(lines, "\n")+"\n")
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func createInitialAdmin(ctx context.Context, data *store.Store, email, password string) error {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	orgID, roleID := id.New(), id.New()
	now := store.NowString()
	return data.CreateOrganizationWithAdmin(ctx,
		domain.Organization{ID: orgID, Name: "rowset", CreatedAt: now},
		domain.Role{ID: roleID, OrgID: orgID, Name: "admin"},
		domain.User{ID: id.New(), OrgID: orgID, Email: strings.ToLower(strings.TrimSpace(email)), PasswordHash: hash, Status: "active", CreatedAt: now},
	)
}

func buildLogger(logDir string) *slog.Logger {
	if strings.TrimSpace(logDir) == "" {
		return slog.Default()
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return slog.Default()
	}
	file, err := os.OpenFile(filepath.Join(logDir, "rowset.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return slog.Default()
	}
	return slog.New(slog.NewJSONHandler(file, &slog.HandlerOptions{Level: slog.LevelInfo}))
}
