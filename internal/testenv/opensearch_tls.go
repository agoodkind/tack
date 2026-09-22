package testenv

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

type openSearchCertificates struct {
	ca   []byte
	node []byte
	key  []byte
}

// makeOpenSearchCertificates issues a test authority and a node certificate
// for address.
func makeOpenSearchCertificates(ctx context.Context, address net.IP) (openSearchCertificates, error) {
	now := clock.Now()
	caSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return openSearchCertificates{}, certificateError(ctx, fmt.Errorf("generate OpenSearch CA serial: %w", err))
	}
	nodeSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return openSearchCertificates{}, certificateError(ctx, fmt.Errorf("generate OpenSearch node serial: %w", err))
	}
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return openSearchCertificates{}, certificateError(ctx, fmt.Errorf("generate OpenSearch CA key: %w", err))
	}
	ca := &x509.Certificate{
		SerialNumber: caSerial, Subject: pkix.Name{CommonName: "tack-testenv-ca"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return openSearchCertificates{}, certificateError(ctx, fmt.Errorf("create OpenSearch CA certificate: %w", err))
	}
	nodeKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return openSearchCertificates{}, certificateError(ctx, fmt.Errorf("generate OpenSearch node key: %w", err))
	}
	node := &x509.Certificate{
		SerialNumber: nodeSerial, Subject: pkix.Name{CommonName: "opensearch-node"},
		IPAddresses: []net.IP{address}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	nodeDER, err := x509.CreateCertificate(rand.Reader, node, ca, &nodeKey.PublicKey, caKey)
	if err != nil {
		return openSearchCertificates{}, certificateError(ctx, fmt.Errorf("create OpenSearch node certificate: %w", err))
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(nodeKey)
	if err != nil {
		return openSearchCertificates{}, certificateError(ctx, fmt.Errorf("encode OpenSearch node key: %w", err))
	}
	return openSearchCertificates{
		ca:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		node: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: nodeDER}),
		key:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

func certificateError(ctx context.Context, err error) error {
	telemetry.L(ctx).ErrorContext(ctx, "search.fixture_certificate_failed", slog.String("err", err.Error()))
	return err
}
