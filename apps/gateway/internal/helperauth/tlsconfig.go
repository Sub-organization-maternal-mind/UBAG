package helperauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// ServerTLSConfig is the helper-plane TLS configuration: TLS 1.3 only, a client
// certificate is required and must chain to clientCAs (the fleet manager CA),
// and the leaf must pass Verify (URI SAN identity, short-lived, pinned SPKI,
// not revoked) before the handshake completes. Session tickets are off so a
// resumed session can never outlive a revocation or a pin change; reconnects pay
// a full handshake, which is cheap at <= nodes.MaxNodes peers.
func (a *Authenticator) ServerTLSConfig(clientCAs *x509.CertPool, getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error)) *tls.Config {
	return &tls.Config{
		MinVersion:             tls.VersionTLS13,
		ClientAuth:             tls.RequireAndVerifyClientCert,
		ClientCAs:              clientCAs,
		GetCertificate:         getCert,
		SessionTicketsDisabled: true,
		Time:                   a.Now,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return a.reject(ReasonNoCert, "", nil)
			}
			_, err := a.Verify(context.Background(), cs.PeerCertificates[0])
			return err
		},
	}
}

// LoadCAPool reads a PEM bundle of trust anchors. It fails on an empty bundle so
// a bad path or file can never silently trust nothing (or everything).
func LoadCAPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read helper CA bundle: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("helper CA bundle %q has no PEM certificates", path)
	}
	return pool, nil
}

// KeyPair serves the listener's own certificate from files and picks up a
// renewed pair (short-lived certs are rotated in place) without a restart. A
// failed reload keeps serving the last good pair.
type KeyPair struct {
	certFile, keyFile string
	reloadEvery       time.Duration

	mu        sync.Mutex
	cert      *tls.Certificate
	certMod   time.Time
	keyMod    time.Time
	lastCheck time.Time
}

// NewKeyPair loads the pair once; a bad pair fails startup.
func NewKeyPair(certFile, keyFile string) (*KeyPair, error) {
	k := &KeyPair{certFile: certFile, keyFile: keyFile, reloadEvery: 5 * time.Second}
	if err := k.load(); err != nil {
		return nil, err
	}
	return k, nil
}

func (k *KeyPair) load() error {
	cert, err := tls.LoadX509KeyPair(k.certFile, k.keyFile)
	if err != nil {
		return fmt.Errorf("load helper plane TLS key pair: %w", err)
	}
	k.cert = &cert
	k.certMod, k.keyMod = modTime(k.certFile), modTime(k.keyFile)
	return nil
}

func modTime(path string) time.Time {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// GetCertificate implements tls.Config.GetCertificate.
func (k *KeyPair) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if now := time.Now(); now.Sub(k.lastCheck) >= k.reloadEvery {
		k.lastCheck = now
		if !modTime(k.certFile).Equal(k.certMod) || !modTime(k.keyFile).Equal(k.keyMod) {
			if err := k.load(); err != nil {
				slog.Warn("helper plane TLS key pair reload failed; keeping the previous pair", "error", err)
				k.certMod, k.keyMod = modTime(k.certFile), modTime(k.keyFile) // retry on the next change, not every handshake
			}
		}
	}
	return k.cert, nil
}
