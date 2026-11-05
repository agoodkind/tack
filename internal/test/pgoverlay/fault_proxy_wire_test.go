//go:build pgoverlay

package pgoverlay_test

import (
	"net"
	"strings"
)

func (proxy *faultProxy) forwardClient(client, backend net.Conn, session *faultSession) {
	for {
		message, err := protocolMessage(client)
		if err != nil {
			return
		}
		proxy.mutex.Lock()
		blocked := proxy.blocked
		if blocked {
			proxy.denied = true
		}
		proxy.mutex.Unlock()
		if blocked {
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
			proxy.mutex.Unlock()
		}
		if _, err := client.Write(message); err != nil {
			return
		}
	}
}
