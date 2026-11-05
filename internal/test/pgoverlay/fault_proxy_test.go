//go:build pgoverlay

package pgoverlay_test

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type faultProxy struct {
	listener net.Listener
	backend  string
	role     string
	mode     string
	mutex    sync.Mutex
	blocked  bool
	fired    bool
	closed   bool
	active   map[net.Conn]struct{}
	workers  sync.WaitGroup
}

type faultSession struct {
	mutex       sync.Mutex
	changedRole bool
	createdRole bool
	captureOID  bool
	oidRow      bool
	commit      bool
	commitDone  bool
}

func newFaultProxy(t *testing.T, target databaseTarget, role, mode string) *faultProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start database fault proxy: %v", err)
	}
	proxy := &faultProxy{listener: listener, backend: net.JoinHostPort(target.host, target.port), role: role, mode: mode, active: make(map[net.Conn]struct{})}
	proxy.workers.Add(1)
	go proxy.accept()
	t.Cleanup(proxy.close)
	return proxy
}

func (proxy *faultProxy) target(original databaseTarget) databaseTarget {
	target := original
	address := proxy.listener.Addr().(*net.TCPAddr)
	target.host = "127.0.0.1"
	target.port = strconv.Itoa(address.Port)
	return target
}

func (proxy *faultProxy) accept() {
	defer proxy.workers.Done()
	for {
		client, err := proxy.listener.Accept()
		if err != nil {
			return
		}
		proxy.mutex.Lock()
		if proxy.blocked || proxy.closed {
			proxy.mutex.Unlock()
			_ = client.Close()
			continue
		}
		proxy.active[client] = struct{}{}
		proxy.workers.Add(1)
		proxy.mutex.Unlock()
		go proxy.serve(client)
	}
}

func (proxy *faultProxy) serve(client net.Conn) {
	defer proxy.workers.Done()
	defer proxy.remove(client)
	backend, err := net.Dial("tcp", proxy.backend)
	if err != nil {
		return
	}
	proxy.mutex.Lock()
	if proxy.closed || proxy.blocked {
		proxy.mutex.Unlock()
		_ = backend.Close()
		return
	}
	proxy.active[backend] = struct{}{}
	proxy.mutex.Unlock()
	defer proxy.remove(backend)

	startup, err := protocolStartup(client)
	if err != nil {
		return
	}
	if _, err = backend.Write(startup); err != nil {
		return
	}
	session := &faultSession{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		proxy.forwardClient(client, backend, session)
		_ = backend.Close()
	}()
	proxy.forwardServer(backend, client, session)
	_ = client.Close()
	<-done
}

func (proxy *faultProxy) forwardClient(client, backend net.Conn, session *faultSession) {
	for {
		message, err := protocolMessage(client)
		if err != nil {
			return
		}
		if message[0] == 'Q' || message[0] == 'P' {
			query := string(message[5:])
			session.mutex.Lock()
			if strings.Contains(query, proxy.role) && strings.Contains(query, "ALTER ROLE") {
				session.changedRole = true
			}
			if strings.Contains(query, proxy.role) && strings.Contains(query, "CREATE ROLE") {
				session.createdRole = true
			}
			if session.createdRole && strings.Contains(query, "SELECT oid::bigint FROM pg_catalog.pg_roles WHERE rolname = $1") {
				session.captureOID = true
			}
			if session.changedRole && strings.Contains(query, "COMMIT") {
				session.commit = true
			}
			session.mutex.Unlock()
		}
		if _, err := backend.Write(message); err != nil {
			return
		}
	}
}

func (proxy *faultProxy) forwardServer(backend, client net.Conn, session *faultSession) {
	for {
		message, err := protocolMessage(backend)
		if err != nil {
			return
		}
		if _, err := client.Write(message); err != nil {
			return
		}
		session.mutex.Lock()
		if message[0] == 'D' && session.captureOID {
			session.oidRow = true
		}
		if message[0] == 'C' && session.commit && strings.HasPrefix(string(message[5:]), "COMMIT") {
			session.commitDone = true
		}
		fire := message[0] == 'Z' && ((proxy.mode == "commit" && session.commitDone) || (proxy.mode == "capture" && session.oidRow))
		session.mutex.Unlock()
		if fire {
			proxy.mutex.Lock()
			proxy.blocked = true
			proxy.fired = true
			for connection := range proxy.active {
				_ = connection.Close()
			}
			proxy.mutex.Unlock()
			return
		}
	}
}

func (proxy *faultProxy) remove(connection net.Conn) {
	_ = connection.Close()
	proxy.mutex.Lock()
	delete(proxy.active, connection)
	proxy.mutex.Unlock()
}

func (proxy *faultProxy) recover(t *testing.T) {
	t.Helper()
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	if !proxy.fired {
		t.Fatal("the database fault proxy did not observe the required protocol boundary")
	}
	proxy.blocked = false
}

func (proxy *faultProxy) close() {
	proxy.mutex.Lock()
	proxy.closed = true
	_ = proxy.listener.Close()
	for connection := range proxy.active {
		_ = connection.Close()
	}
	proxy.mutex.Unlock()
	proxy.workers.Wait()
}
