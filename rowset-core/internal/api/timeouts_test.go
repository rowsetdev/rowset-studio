package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
)

func TestPolicyTimeoutTakesShortestDeadline(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	ctx, cancel := withConnectionTimeout(request, domain.Connection{QueryTimeoutSeconds: 30}, 2, 10*time.Minute)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("deadline missing")
	}
	remaining := deadline.Sub(started)
	if remaining < 1900*time.Millisecond || remaining > 2100*time.Millisecond {
		t.Fatalf("unexpected effective timeout: %s", remaining)
	}
}

func TestPoolSizeUsesPerEngineConfig(t *testing.T) {
	server := &Server{}
	server.config.PostgresPoolSize = 11
	server.config.MySQLPoolSize = 12
	server.config.MSSQLPoolSize = 13
	if server.poolSize("postgres") != 11 || server.poolSize("mariadb") != 12 || server.poolSize("sqlserver") != 13 {
		t.Fatal("engine-specific pool size was not selected")
	}
}
