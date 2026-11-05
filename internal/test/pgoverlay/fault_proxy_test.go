//go:build pgoverlay

package pgoverlay_test

import (
	"net"
	"strconv"
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
	denied   bool
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
			if proxy.blocked {
				proxy.denied = true
			}
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
	backend, err := net.DialTimeout("tcp", proxy.backend, databaseTimeout)
	if err != nil {
		return
	}
	proxy.mutex.Lock()
	if proxy.closed || proxy.blocked {
		if proxy.blocked {
			proxy.denied = true
		}
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
	if !proxy.fired || !proxy.denied {
		t.Fatal("the database fault proxy did not observe the boundary and deny a later operation")
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
