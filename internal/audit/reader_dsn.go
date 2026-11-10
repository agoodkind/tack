package audit

import (
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

var errReaderDSNParse = errors.New("audit: the reader connection string does not parse")

func parseReaderDSN(dsn string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// Omit the pgx parse error because malformed input can expose part of a
		// password.
		return nil, errReaderDSNParse
	}

	return cfg, nil
}
