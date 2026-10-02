package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/moby/moby/api/types/container"
)

func probeCapturedLedger(ctx context.Context, inspected container.InspectResponse) (string, error) {
	address, err := engineAddress(inspected)
	if err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.evidence_address_failed", slog.String("err", err.Error()))
		return "", err
	}
	if inspected.Config == nil {
		return "", fmt.Errorf("captured ledger has no container configuration")
	}
	values := make(map[string]string)
	for _, variable := range inspected.Config.Env {
		key, value, _ := strings.Cut(variable, "=")
		values[key] = value
	}
	for _, key := range []string{"YSQL_USER", "YSQL_PASSWORD", "YSQL_DB"} {
		if values[key] == "" {
			return "", fmt.Errorf("captured ledger is missing %s", key)
		}
	}
	dsn := url.URL{
		Scheme: "postgres", User: url.UserPassword(values["YSQL_USER"], values["YSQL_PASSWORD"]),
		Host: net.JoinHostPort(address, ledgerPort), Path: "/" + values["YSQL_DB"], RawQuery: "sslmode=disable",
	}
	probeContext, cancel := context.WithTimeout(ctx, ledgerProbeTimeout)
	defer cancel()
	connection, err := pgx.Connect(probeContext, dsn.String())
	if err != nil {
		return "", fmt.Errorf("connect captured ledger: %s", strings.ReplaceAll(err.Error(), values["YSQL_PASSWORD"], "[redacted]"))
	}
	defer func() { _ = connection.Close(context.WithoutCancel(ctx)) }()
	var answer int
	var actualAddress string
	if err := connection.QueryRow(probeContext, "SELECT 1, host(inet_server_addr())").Scan(&answer, &actualAddress); err != nil {
		return "", fmt.Errorf("query captured ledger: %w", err)
	}
	if answer != 1 || actualAddress != address {
		return "", fmt.Errorf("captured ledger SQL identity is %s, want %s", actualAddress, address)
	}
	return actualAddress, nil
}
