package testenv

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	"goodkind.io/tack/internal/clock"
)

// clusterNodeCommonName is the subject of every member certificate. The
// members' plugins.security.nodes_dn setting accepts exactly this subject.
const clusterNodeCommonName = "opensearch-node"

// clusterAuthority is one cluster's certificate authority. It keeps its
// private key and signs the certificate of each later member and of the
// proxy with that key. The clients and the earlier members already trust
// this authority.
type clusterAuthority struct {
	certificate *x509.Certificate
	key         *rsa.PrivateKey
	pem         []byte
}

// issuedCertificate is one PEM certificate and its PEM private key.
type issuedCertificate struct {
	certificate []byte
	key         []byte
}

func newClusterAuthority(ctx context.Context) (clusterAuthority, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return clusterAuthority{}, certificateError(ctx, fmt.Errorf("generate cluster CA key: %w", err))
	}
	template, err := certificateTemplate(ctx, "tack-testenv-cluster-ca", nil)
	if err != nil {
		return clusterAuthority{}, err
	}
	template.IsCA, template.BasicConstraintsValid, template.KeyUsage = true, true, x509.KeyUsageCertSign
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return clusterAuthority{}, certificateError(ctx, fmt.Errorf("create cluster CA certificate: %w", err))
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return clusterAuthority{}, certificateError(ctx, fmt.Errorf("parse cluster CA certificate: %w", err))
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return clusterAuthority{certificate: certificate, key: key, pem: encoded}, nil
}

// issue signs a server and client certificate for commonName that is valid
// for every name in dnsNames.
func (a clusterAuthority) issue(ctx context.Context, commonName string, dnsNames []string) (issuedCertificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return issuedCertificate{}, certificateError(ctx, fmt.Errorf("generate key for %s: %w", commonName, err))
	}
	template, err := certificateTemplate(ctx, commonName, dnsNames)
	if err != nil {
		return issuedCertificate{}, err
	}
	template.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
	template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	der, err := x509.CreateCertificate(rand.Reader, template, a.certificate, &key.PublicKey, a.key)
	if err != nil {
		return issuedCertificate{}, certificateError(ctx, fmt.Errorf("sign certificate for %s: %w", commonName, err))
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return issuedCertificate{}, certificateError(ctx, fmt.Errorf("encode key for %s: %w", commonName, err))
	}
	return issuedCertificate{
		certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		key:         pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

// certificateTemplate returns a one-day certificate template with a random
// 128-bit serial number.
func certificateTemplate(ctx context.Context, commonName string, dnsNames []string) (*x509.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, certificateError(ctx, fmt.Errorf("generate serial for %s: %w", commonName, err))
	}
	now := clock.Now()
	return &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: commonName}, DNSNames: dnsNames,
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
	}, nil
}
