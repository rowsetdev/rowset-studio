package engine

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startTestSSHServer runs an in-process SSH server that authenticates one
// user/password and forwards the port-forward channels its clients open to
// the address they ask for. It returns the server address, its host key line,
// and a count of connections it forwarded.
func startTestSSHServer(t *testing.T, user, password string) (addr, hostKeyLine string, forwarded *int64) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, given []byte) (*ssh.Permissions, error) {
			if meta.User() == user && string(given) == password {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("denied")
		},
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	var mu sync.Mutex
	add := func() { mu.Lock(); count++; mu.Unlock() }

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveSSHConn(conn, config, add)
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })

	line := signer.PublicKey().Type() + " " + base64.StdEncoding.EncodeToString(signer.PublicKey().Marshal())
	return listener.Addr().String(), line, &count
}

func serveSSHConn(conn net.Conn, config *ssh.ServerConfig, forwarded func()) {
	serverConn, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer serverConn.Close()
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "direct-tcpip" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only port forwarding")
			continue
		}
		var payload struct {
			DestAddr string
			DestPort uint32
			OrigAddr string
			OrigPort uint32
		}
		if err := ssh.Unmarshal(newChannel.ExtraData(), &payload); err != nil {
			_ = newChannel.Reject(ssh.ConnectionFailed, "bad forward request")
			continue
		}
		target, err := net.DialTimeout("tcp", net.JoinHostPort(payload.DestAddr, strconv.Itoa(int(payload.DestPort))), 5*time.Second)
		if err != nil {
			_ = newChannel.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		channel, reqs, err := newChannel.Accept()
		if err != nil {
			_ = target.Close()
			continue
		}
		forwarded()
		go ssh.DiscardRequests(reqs)
		go func() {
			defer channel.Close()
			defer target.Close()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(target, channel); done <- struct{}{} }()
			go func() { _, _ = io.Copy(channel, target); done <- struct{}{} }()
			<-done
		}()
	}
}

// TestLiveDatabaseThroughSSHTunnel reaches each database through the tunnel
// and checks host-key verification: the right key connects, a wrong one is
// refused, and none accepts a connection with no key trusted.
func TestLiveDatabaseThroughSSHTunnel(t *testing.T) {
	sqlEngines := []struct {
		engine, passwordEnv, user string
		port                      int
	}{
		{"postgres", "ROWSET_MATRIX_POSTGRES_PASSWORD", "postgres", 55432},
		{"mysql", "ROWSET_MATRIX_MYSQL_PASSWORD", "root", 53306},
		{"mariadb", "ROWSET_MATRIX_MARIADB_PASSWORD", "root", 53307},
		{"mssql", "ROWSET_MATRIX_MSSQL_PASSWORD", "sa", 51433},
	}
	for _, e := range sqlEngines {
		e := e
		t.Run(e.engine, func(t *testing.T) {
			password := os.Getenv(e.passwordEnv)
			if password == "" {
				t.Skip(e.passwordEnv + " is not configured")
			}
			testSSHTunnel(t, e.engine, func(host string, port int) Connection {
				return Connection{Engine: e.engine, Host: "127.0.0.1", Port: e.port, Database: "rowset_e2e", Username: e.user, Password: password, PoolSize: 1}
			})
		})
	}

	// MongoDB, Redis/Valkey and Elasticsearch have no query path through
	// Manager.Execute; Manager.Test (a Ping/equivalent) is what every one of
	// them, SQL included, is actually verified through here.
	docEngines := []struct {
		engine, hostEnv, portEnv string
		defaultPort              int
		database                 string
	}{
		{"mongodb", "ROWSET_TEST_MONGODB_HOST", "ROWSET_MATRIX_MONGODB_PORT", 27017, ""},
		{"redis", "ROWSET_TEST_REDIS_HOST", "ROWSET_TEST_REDIS_PORT", 6379, "0"},
		{"elasticsearch", "ROWSET_TEST_ELASTICSEARCH_HOST", "ROWSET_TEST_ELASTICSEARCH_PORT", 9200, "elasticsearch"},
	}
	for _, e := range docEngines {
		e := e
		t.Run(e.engine, func(t *testing.T) {
			host := os.Getenv(e.hostEnv)
			if host == "" {
				t.Skip(e.hostEnv + " is not configured")
			}
			port := e.defaultPort
			if v := os.Getenv(e.portEnv); v != "" {
				var err error
				if port, err = parsePort(v); err != nil {
					t.Fatal(err)
				}
			}
			testSSHTunnel(t, e.engine, func(sshHost string, sshPort int) Connection {
				return Connection{Engine: e.engine, Host: host, Port: port, Database: e.database, TLS: TLSSettings{Mode: TLSDisable}, PoolSize: 1}
			})
		})
	}
}

// testSSHTunnel starts an in-process SSH server and runs the standard
// host-key-trust and tunnel-forwarding checks against build's connection
// (its Host/Port/etc. reach the real target directly; SSH fields are added
// here).
func testSSHTunnel(t *testing.T, engineName string, build func(sshHost string, sshPort int) Connection) {
	t.Helper()
	sshAddr, hostKey, forwarded := startTestSSHServer(t, "tunnel", "tunnelpass")
	host, portText, _ := net.SplitHostPort(sshAddr)
	sshPort, _ := strconv.Atoi(portText)
	base := SSHConfig{Host: host, Port: sshPort, User: "tunnel", AuthMethod: "password", Password: "tunnelpass"}

	manager := NewManager()
	defer manager.Close()
	connect := func(known string) error {
		conn := build(host, sshPort)
		conn.ID = "ssh-" + engineName + "-" + known[:min(len(known), 8)]
		ssh := base
		ssh.KnownHost = known
		conn.SSH = ssh
		return manager.Test(context.Background(), conn)
	}

	// With no host key trusted, a connection is refused before dialing.
	if err := connect(""); err == nil || !strings.Contains(err.Error(), "host key has not been accepted") {
		t.Fatalf("connection without a trusted host key was allowed: %v", err)
	}
	// The tunnel-only test learns the key; it must match the server's.
	learnConn := build(host, sshPort)
	learnConn.SSH = base
	learned, err := manager.TestTunnel(context.Background(), learnConn)
	if err != nil || learned != hostKey {
		t.Fatalf("TestTunnel key=%q want %q err=%v", learned, hostKey, err)
	}
	// A wrong host key is refused.
	wrong := hostKey[:len(hostKey)-6] + "AAAAAA"
	if err := connect(wrong); err == nil {
		t.Fatal("a wrong host key was accepted")
	}
	// The learned key connects and the check runs through the tunnel.
	if err := connect(hostKey); err != nil {
		t.Fatalf("connection through the tunnel failed: %v", err)
	}
	if *forwarded == 0 {
		t.Fatal("no connection was forwarded through the SSH server")
	}
}
