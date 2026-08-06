package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgproto3"
	"goodkind.io/tack/internal/config"
)

func TestAppRuntimeDoesNotStartNotarizer(t *testing.T) {
	server := newAuditPostgresServer(t)
	ctx := context.Background()
	runtime := buildAuditRuntime(ctx, &config.Config{
		AuditKafkaBrokers:   "127.0.0.1:1",
		AuditKafkaTopic:     "audit.events.v1",
		AuditSigningKeyPath: writeAuditSigningKey(t),
		AuditWriterDSN:      server.DSN(),
	})
	t.Cleanup(runtime.Close)

	if got := server.connections.Load(); got != 0 {
		t.Fatalf("app runtime opened %d notarizer connections, want 0", got)
	}
}

type auditPostgresServer struct {
	listener    net.Listener
	connections atomic.Int64
}

func newAuditPostgresServer(t *testing.T) *auditPostgresServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &auditPostgresServer{listener: listener}
	go server.serve()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

func (s *auditPostgresServer) DSN() string {
	return fmt.Sprintf(
		"postgres://test@%s/test?sslmode=disable&default_query_exec_mode=simple_protocol",
		s.listener.Addr().String(),
	)
}

func (s *auditPostgresServer) serve() {
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.connections.Add(1)
		go serveAuditPostgresConnection(connection)
	}
}

func serveAuditPostgresConnection(connection net.Conn) {
	defer func() { _ = connection.Close() }()
	backend := pgproto3.NewBackend(connection, connection)
	if _, err := backend.ReceiveStartupMessage(); err != nil {
		return
	}
	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
	backend.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
	backend.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: make([]byte, 4)})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if err := backend.Flush(); err != nil {
		return
	}
	for {
		message, err := backend.Receive()
		if err != nil {
			return
		}
		switch message.(type) {
		case *pgproto3.Query:
			backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{}})
			backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 0")})
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			if backend.Flush() != nil {
				return
			}
		case *pgproto3.Terminate:
			return
		}
	}
}

func writeAuditSigningKey(t *testing.T) string {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal signing key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "audit-signing.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write signing key: %v", err)
	}
	return path
}
