package config

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var defaultEncryptionKey = []byte{
	7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
	7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
}

type Environment string

const (
	Development Environment = "development"
	Production  Environment = "production"
)

type Config struct {
	// Desktop-only process capabilities, never loaded from a configuration file.
	LocalLauncherKey string
	LocalOwnerEmail  string
	LocalShutdown    func()
	// Shared marks a multi-user installation. The default is a personal
	// workspace that only listens on the loopback interface.
	Shared                    bool
	Environment               Environment
	BindHost                  string
	APIPort                   uint16
	JWTSecret                 string
	JWTSecretPrevious         string
	EncryptionKey             []byte
	EncryptionKeyPrevious     []byte
	DBPath                    string
	BootstrapAdminPassword    string
	StudioOrigin              string
	RequestBodyLimitBytes     int64
	AuditRetentionDays        *uint32
	QueryHistoryRetentionDays *uint32
	// RetentionConfigured is true when either retention period was set
	// explicitly. A personal workspace deletes nothing unless it was.
	RetentionConfigured bool
	LogDir              string
	// ScheduleOutputDir holds the result files of scheduled queries on a
	// shared server, one folder per user; empty means next to the database.
	ScheduleOutputDir         string
	SecureCookies             bool
	RateLimitPerMinute        uint32
	TopologyCheckIntervalSecs uint64
	QueryStreamTimeoutSecs    uint64
	PostgresPoolSize          uint32
	MySQLPoolSize             uint32
	MSSQLPoolSize             uint32
}

func Load() (Config, error) {
	if err := loadEnvFile(); err != nil {
		return Config{}, err
	}
	cfg, err := FromEnvironment("rowset-community.sqlite3")
	if err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// LoadEnvironment loads the configuration file into the environment and reads
// it without validating the result.
func LoadEnvironment(defaultDBPath string) (Config, error) {
	if err := loadEnvFile(); err != nil {
		return Config{}, err
	}
	return FromEnvironment(defaultDBPath)
}

// FromEnvironment reads the process environment without validating it.
func FromEnvironment(defaultDBPath string) (Config, error) {
	env := Environment(get("ROWSET_ENV", string(Development)))
	if env != Development && env != Production {
		return Config{}, fmt.Errorf("invalid ROWSET_ENV: %s", env)
	}
	key, err := decodeKey(get("ROWSET_ENC_KEY", base64.StdEncoding.EncodeToString(defaultEncryptionKey)))
	if err != nil {
		return Config{}, fmt.Errorf("ROWSET_ENC_KEY: %w", err)
	}
	var previous []byte
	if raw := strings.TrimSpace(os.Getenv("ROWSET_ENC_KEY_PREVIOUS")); raw != "" {
		previous, err = decodeKey(raw)
		if err != nil {
			return Config{}, fmt.Errorf("ROWSET_ENC_KEY_PREVIOUS: %w", err)
		}
	}
	return Config{
		Environment: env, BindHost: get("ROWSET_BIND_HOST", "127.0.0.1"), APIPort: uint16Value("ROWSET_API_PORT", 8080),
		JWTSecret: get("ROWSET_JWT_SECRET", "dev-only-change-me"), JWTSecretPrevious: optional("ROWSET_JWT_SECRET_PREVIOUS"),
		EncryptionKey: key, EncryptionKeyPrevious: previous, DBPath: get("ROWSET_DB_PATH", defaultDBPath),
		BootstrapAdminPassword: optional("ROWSET_BOOTSTRAP_ADMIN_PASSWORD"), StudioOrigin: strings.TrimSuffix(optional("ROWSET_STUDIO_ORIGIN"), "/"),
		RequestBodyLimitBytes: int64Value("ROWSET_BODY_LIMIT_BYTES", 1_048_576),
		AuditRetentionDays:    retention("ROWSET_AUDIT_RETENTION_DAYS", 90), QueryHistoryRetentionDays: retention("ROWSET_QUERY_HISTORY_RETENTION_DAYS", 30),
		RetentionConfigured: strings.TrimSpace(os.Getenv("ROWSET_AUDIT_RETENTION_DAYS")) != "" || strings.TrimSpace(os.Getenv("ROWSET_QUERY_HISTORY_RETENTION_DAYS")) != "",
		LogDir:              optional("ROWSET_LOG_DIR"), ScheduleOutputDir: optional("ROWSET_SCHEDULE_OUTPUT_DIR"), SecureCookies: boolValue("ROWSET_SECURE_COOKIES", env == Production),
		RateLimitPerMinute: uint32(uint64Value("ROWSET_RATE_LIMIT_PER_MINUTE", 120)), TopologyCheckIntervalSecs: uint64Value("ROWSET_TOPOLOGY_CHECK_INTERVAL_SECS", 30),
		QueryStreamTimeoutSecs: uint64Value("ROWSET_QUERY_STREAM_TIMEOUT_SECS", 8*60),
		PostgresPoolSize:       uint32(uint64Value("ROWSET_POSTGRES_POOL_SIZE", 10)), MySQLPoolSize: uint32(uint64Value("ROWSET_MYSQL_POOL_SIZE", 10)),
		MSSQLPoolSize: uint32(uint64Value("ROWSET_MSSQL_POOL_SIZE", 10)),
	}, nil
}

func (c Config) Validate() error {
	if !c.Shared {
		ip := net.ParseIP(c.BindHost)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("a personal workspace must bind to a loopback IP address")
		}
	}
	if len(c.JWTSecret) < 32 || c.JWTSecret == "dev-only-change-me" {
		return errors.New("ROWSET_JWT_SECRET must be a unique secret of at least 32 characters")
	}
	if len(c.EncryptionKey) != 32 {
		return errors.New("ROWSET_ENC_KEY must decode to exactly 32 bytes")
	}
	if string(c.EncryptionKey) == string(defaultEncryptionKey) {
		return errors.New("ROWSET_ENC_KEY must be unique")
	}
	if c.BootstrapAdminPassword != "" && len(c.BootstrapAdminPassword) < 12 {
		return errors.New("ROWSET_BOOTSTRAP_ADMIN_PASSWORD must be at least 12 characters")
	}
	if c.RequestBodyLimitBytes <= 0 {
		return errors.New("ROWSET_BODY_LIMIT_BYTES must be positive")
	}
	if c.PostgresPoolSize == 0 || c.PostgresPoolSize > 1000 || c.MySQLPoolSize == 0 || c.MySQLPoolSize > 1000 || c.MSSQLPoolSize == 0 || c.MSSQLPoolSize > 1000 {
		return errors.New("ROWSET_*_POOL_SIZE must be between 1 and 1000")
	}
	return nil
}

func loadEnvFile() error {
	candidates := []string{os.Getenv("ROWSET_CONFIG"), "rowset.env"}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "rowset.env"))
	}
	for _, path := range candidates {
		if path == "" {
			continue
		}
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("open config %s: %w", path, err)
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			key = strings.TrimSpace(key)
			if !ok || key == "" {
				continue
			}
			if _, exists := os.LookupEnv(key); exists {
				continue
			}
			value = strings.Trim(strings.TrimSpace(value), "\"")
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
		return scanner.Err()
	}
	return nil
}

func decodeKey(raw string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) != 32 {
		return nil, errors.New("must be valid base64 decoding to 32 bytes")
	}
	return key, nil
}
func get(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
func optional(key string) string { return strings.TrimSpace(os.Getenv(key)) }
func uint16Value(key string, fallback uint16) uint16 {
	value, err := strconv.ParseUint(os.Getenv(key), 10, 16)
	if err != nil {
		return fallback
	}
	return uint16(value)
}
func uint64Value(key string, fallback uint64) uint64 {
	value, err := strconv.ParseUint(os.Getenv(key), 10, 64)
	if err != nil {
		return fallback
	}
	return value
}
func int64Value(key string, fallback int64) int64 {
	value, err := strconv.ParseInt(os.Getenv(key), 10, 64)
	if err != nil {
		return fallback
	}
	return value
}
func boolValue(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes"
}
func retention(key string, fallback uint32) *uint32 {
	value := uint64Value(key, uint64(fallback))
	if value == 0 {
		return nil
	}
	result := uint32(value)
	return &result
}
