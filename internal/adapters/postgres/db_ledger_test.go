package postgres

import (
	"net"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgproto3"
)

// fakeLedger answers PostgreSQL startup and the pool's empty Query ping.
// goSilent stops replies without closing accepted sockets.
type fakeLedger struct {
	listener   net.Listener
	silent     chan struct{}
	done       chan struct{}
	silentOnce sync.Once
	doneOnce   sync.Once
	mu         sync.Mutex
	accepted   []net.Conn
}

func newFakeLedger(t *testing.T) *fakeLedger {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ledger := &fakeLedger{
		listener: listener,
		silent:   make(chan struct{}),
		done:     make(chan struct{}),
	}
	go ledger.serve()
	t.Cleanup(ledger.shutdown)
	return ledger
}

// shutdown closes every accepted socket. Tests must shut down a silent ledger
// before closing its pool to avoid the driver's per-connection termination timeout.
func (ledger *fakeLedger) shutdown() {
	ledger.doneOnce.Do(func() { close(ledger.done) })
	_ = ledger.listener.Close()
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	for _, connection := range ledger.accepted {
		_ = connection.Close()
	}
	ledger.accepted = nil
}

func (ledger *fakeLedger) addr() string {
	return ledger.listener.Addr().String()
}

func (ledger *fakeLedger) goSilent() {
	ledger.silentOnce.Do(func() { close(ledger.silent) })
}

func (ledger *fakeLedger) isSilent() bool {
	select {
	case <-ledger.silent:
		return true
	default:
		return false
	}
}

func (ledger *fakeLedger) serve() {
	for {
		connection, err := ledger.listener.Accept()
		if err != nil {
			return
		}
		ledger.mu.Lock()
		ledger.accepted = append(ledger.accepted, connection)
		ledger.mu.Unlock()
		go ledger.serveConnection(connection)
	}
}

func (ledger *fakeLedger) serveConnection(connection net.Conn) {
	defer func() { _ = connection.Close() }()
	backend := pgproto3.NewBackend(connection, connection)
	if _, err := backend.ReceiveStartupMessage(); err != nil {
		return
	}
	if ledger.isSilent() {
		<-ledger.done
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
			if ledger.isSilent() {
				// Wait for shutdown without closing the socket or replying.
				<-ledger.done
				return
			}
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
