package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in: ROWSET_TLS_DIR holds ca.crt, other-ca.crt, client.crt and
// client.key. The servers present certificates for localhost/127.0.0.1 signed
// by ca.crt: PostgreSQL on ROWSET_TLS_POSTGRES_PORT (hostssl cert auth for
// "certuser"), MySQL on 53306 ("certuser" REQUIRE X509) and SQL Server on 51433.
func TestLiveTLSModesAgainstRealServers(t *testing.T) {
	dir := os.Getenv("ROWSET_TLS_DIR")
	if dir == "" {
		t.Skip("ROWSET_TLS_DIR is not configured")
	}
	read := func(name string) string {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	ca, other, clientCert, clientKey := read("ca.crt"), read("other-ca.crt"), read("client.crt"), read("client.key")
	pgPort := 55433
	if raw := os.Getenv("ROWSET_TLS_POSTGRES_PORT"); raw != "" {
		_, _ = fmt.Sscan(raw, &pgPort)
	}
	pg := func(host, user string, settings TLSSettings) Connection {
		return Connection{Engine: "postgres", Host: host, Port: pgPort, Database: "rowset_e2e", Username: user, Password: os.Getenv("ROWSET_MATRIX_POSTGRES_PASSWORD"), TLS: settings}
	}
	my := func(user string, settings TLSSettings) Connection {
		password := os.Getenv("ROWSET_MATRIX_MYSQL_PASSWORD")
		if user != "root" {
			password = ""
		}
		return Connection{Engine: "mysql", Host: "127.0.0.1", Port: 53306, Database: "rowset_e2e", Username: user, Password: password, TLS: settings}
	}
	ms := func(settings TLSSettings) Connection {
		return Connection{Engine: "mssql", Host: "127.0.0.1", Port: 51433, Database: "rowset_e2e", Username: "sa", Password: os.Getenv("ROWSET_MATRIX_MSSQL_PASSWORD"), TLS: settings}
	}
	const pgSSL = "SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()"
	const mySSL = "SELECT variable_value <> '' FROM performance_schema.session_status WHERE variable_name = 'Ssl_version'"
	const msSSL = "SELECT CASE encrypt_option WHEN 'TRUE' THEN 1 ELSE 0 END FROM sys.dm_exec_connections WHERE session_id = @@SPID"
	cases := []struct {
		name       string
		connection Connection
		probe      string
		ok         bool
		encrypted  bool
	}{
		{"postgres verify-full IP SAN", pg("127.0.0.1", "postgres", TLSSettings{Mode: TLSVerifyFull, CAPEM: ca}), pgSSL, true, true},
		{"postgres verify-full DNS SAN", pg("localhost", "postgres", TLSSettings{Mode: TLSVerifyFull, CAPEM: ca}), pgSSL, true, true},
		{"postgres verify-full untrusted CA", pg("127.0.0.1", "postgres", TLSSettings{Mode: TLSVerifyFull, CAPEM: other}), pgSSL, false, false},
		{"postgres verify-full wrong server name", pg("127.0.0.1", "postgres", TLSSettings{Mode: TLSVerifyFull, CAPEM: ca, ServerName: "db.other"}), pgSSL, false, false},
		{"postgres verify-ca ignores name", pg("127.0.0.1", "postgres", TLSSettings{Mode: TLSVerifyCA, CAPEM: ca, ServerName: "db.other"}), pgSSL, true, true},
		{"postgres verify-ca untrusted CA", pg("127.0.0.1", "postgres", TLSSettings{Mode: TLSVerifyCA, CAPEM: other}), pgSSL, false, false},
		{"postgres require", pg("127.0.0.1", "postgres", TLSSettings{Mode: TLSRequire}), pgSSL, true, true},
		{"postgres client certificate", pg("127.0.0.1", "certuser", TLSSettings{Mode: TLSVerifyFull, CAPEM: ca, ClientCertPEM: clientCert, ClientKeyPEM: clientKey}), pgSSL, true, true},
		{"postgres missing client certificate", pg("127.0.0.1", "certuser", TLSSettings{Mode: TLSVerifyFull, CAPEM: ca}), pgSSL, false, false},

		{"mysql verify-full", my("root", TLSSettings{Mode: TLSVerifyFull, CAPEM: ca}), mySSL, true, true},
		{"mysql verify-full untrusted CA", my("root", TLSSettings{Mode: TLSVerifyFull, CAPEM: other}), mySSL, false, false},
		{"mysql verify-full wrong server name", my("root", TLSSettings{Mode: TLSVerifyFull, CAPEM: ca, ServerName: "db.other"}), mySSL, false, false},
		{"mysql verify-ca ignores name", my("root", TLSSettings{Mode: TLSVerifyCA, CAPEM: ca, ServerName: "db.other"}), mySSL, true, true},
		{"mysql verify-ca untrusted CA", my("root", TLSSettings{Mode: TLSVerifyCA, CAPEM: other}), mySSL, false, false},
		{"mysql require", my("root", TLSSettings{Mode: TLSRequire}), mySSL, true, true},
		{"mysql client certificate", my("certuser", TLSSettings{Mode: TLSVerifyFull, CAPEM: ca, ClientCertPEM: clientCert, ClientKeyPEM: clientKey}), mySSL, true, true},
		{"mysql missing client certificate", my("certuser", TLSSettings{Mode: TLSVerifyFull, CAPEM: ca}), mySSL, false, false},
		{"mysql disable", my("root", TLSSettings{Mode: TLSDisable}), mySSL, true, false},

		{"mssql verify-full", ms(TLSSettings{Mode: TLSVerifyFull, CAPEM: ca}), msSSL, true, true},
		{"mssql verify-full untrusted CA", ms(TLSSettings{Mode: TLSVerifyFull, CAPEM: other}), msSSL, false, false},
		{"mssql verify-full system roots", ms(TLSSettings{Mode: TLSVerifyFull}), msSSL, false, false},
		{"mssql verify-full wrong server name", ms(TLSSettings{Mode: TLSVerifyFull, CAPEM: ca, ServerName: "db.other"}), msSSL, false, false},
		{"mssql verify-ca ignores name", ms(TLSSettings{Mode: TLSVerifyCA, CAPEM: ca, ServerName: "db.other"}), msSSL, true, true},
		{"mssql verify-ca untrusted CA", ms(TLSSettings{Mode: TLSVerifyCA, CAPEM: other}), msSSL, false, false},
		{"mssql require", ms(TLSSettings{Mode: TLSRequire}), msSSL, true, true},
		{"mssql disable", ms(TLSSettings{Mode: TLSDisable}), msSSL, true, false},
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			manager := NewManager()
			defer manager.Close()
			test.connection.ID = fmt.Sprintf("tls-%d", index)
			result, err := manager.Execute(ctx, test.connection, test.probe, 1)
			if (err == nil) != test.ok {
				t.Fatalf("err = %v, want ok=%v", err, test.ok)
			}
			if err != nil {
				t.Logf("rejected as expected: %v", err)
				return
			}
			encrypted := fmt.Sprint(result.Rows[0][0])
			if (encrypted == "true" || encrypted == "1") != test.encrypted {
				t.Fatalf("session encrypted = %s, want %v", encrypted, test.encrypted)
			}
		})
	}

	// Result column origins on MySQL come from a second client library;
	// it must negotiate the same TLS settings, including client certificates.
	t.Run("mysql metadata client uses TLS settings", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		manager := NewManager()
		defer manager.Close()
		root := my("root", TLSSettings{Mode: TLSVerifyFull, CAPEM: ca})
		root.ID = "tls-origin-setup"
		for _, statement := range []string{"DROP TABLE IF EXISTS rowset_tls_origin", "CREATE TABLE rowset_tls_origin(id int primary key, email varchar(40))"} {
			if _, err := manager.Execute(ctx, root, statement, 0); err != nil {
				t.Fatal(err)
			}
		}
		for name, connection := range map[string]Connection{
			"verify-full":        root,
			"client certificate": my("certuser", TLSSettings{Mode: TLSVerifyFull, CAPEM: ca, ClientCertPEM: clientCert, ClientKeyPEM: clientKey}),
		} {
			origins := manager.mysqlColumnOrigins(ctx, connection, "SELECT email FROM rowset_tls_origin WHERE id > 0")
			if len(origins) != 1 || !origins[0].Resolved || origins[0].Table != "rowset_tls_origin" || origins[0].Column != "email" {
				t.Fatalf("%s: origins = %#v", name, origins)
			}
		}
		if origins := manager.mysqlColumnOrigins(ctx, my("root", TLSSettings{Mode: TLSVerifyFull, CAPEM: other}), "SELECT email FROM rowset_tls_origin WHERE id > 0"); origins != nil {
			t.Fatalf("metadata client ignored certificate verification: %#v", origins)
		}
	})
}
