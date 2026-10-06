// Package authtest generates throwaway certificate authorities and Helper Node
// leaf certificates in memory for tests of the helper trust plane. Nothing here
// is used at runtime and no key ever touches the repository.
package authtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// CA is a self-signed test authority.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
	PEM  []byte
	Pool *x509.CertPool
}

// NewCA creates a fresh authority valid around the real clock.
func NewCA(t testing.TB) *CA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: "UBAG test helper CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &CA{Cert: cert, Key: key, Pool: pool, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// Spec describes one leaf. Zero values give a well-formed, valid node cert:
// CN = NodeID, URI SAN spiffe://ubag/node/<NodeID>, clientAuth+serverAuth,
// valid for one hour. Fields override one defect at a time for negative tests.
type Spec struct {
	NodeID    string
	CN        string             // default NodeID
	URIs      []string           // default [spiffe://ubag/node/<NodeID>]; set to override (an empty non-nil slice means none)
	EKUs      []x509.ExtKeyUsage // default clientAuth + serverAuth
	NotBefore time.Time
	NotAfter  time.Time
	IsCA      bool
	Key       *ecdsa.PrivateKey // reuse a key (same SPKI); default fresh
}

// Leaf is an issued certificate with its key.
type Leaf struct {
	TLS     tls.Certificate
	X509    *x509.Certificate
	SPKI    string // lowercase hex SHA-256 of the SubjectPublicKeyInfo (the registry pin)
	CertPEM []byte
	KeyPEM  []byte
}

// Issue signs a leaf.
func (c *CA) Issue(t testing.TB, s Spec) *Leaf {
	t.Helper()
	key := s.Key
	if key == nil {
		var err error
		if key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			t.Fatal(err)
		}
	}
	if s.CN == "" {
		s.CN = s.NodeID
	}
	if s.URIs == nil {
		s.URIs = []string{"spiffe://ubag/node/" + s.NodeID}
	}
	if s.EKUs == nil {
		s.EKUs = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}
	}
	if s.NotBefore.IsZero() {
		s.NotBefore = time.Now().Add(-time.Minute)
	}
	if s.NotAfter.IsZero() {
		s.NotAfter = time.Now().Add(time.Hour)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: s.CN},
		NotBefore:             s.NotBefore,
		NotAfter:              s.NotAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           s.EKUs,
		IsCA:                  s.IsCA,
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}
	for _, u := range s.URIs {
		parsed, err := url.Parse(u)
		if err != nil {
			t.Fatal(err)
		}
		tmpl.URIs = append(tmpl.URIs, parsed)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.Cert, &key.PublicKey, c.Key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(parsed.RawSubjectPublicKeyInfo)
	return &Leaf{
		TLS:     tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: parsed},
		X509:    parsed,
		SPKI:    hex.EncodeToString(sum[:]),
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}
}

// WriteFiles writes the PEMs under dir and returns their paths.
func (l *Leaf) WriteFiles(t testing.TB, dir, base string) (certFile, keyFile string) {
	t.Helper()
	certFile, keyFile = filepath.Join(dir, base+".crt"), filepath.Join(dir, base+".key")
	if err := os.WriteFile(certFile, l.CertPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, l.KeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// WriteCA writes the CA bundle under dir and returns its path.
func (c *CA) WriteCA(t testing.TB, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, c.PEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func serial(t testing.TB) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	if err != nil {
		t.Fatal(err)
	}
	return n
}
