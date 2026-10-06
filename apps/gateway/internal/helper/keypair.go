package helper

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"
)

func spkiHex(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
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

// OwnCertCheck returns the profile check for the helper's own server
// certificate: the one the primary will pin and verify. It must carry exactly
// the URI SAN of nodeID and the serverAuth EKU, be valid now and live at most
// 72h. A helper whose certificate names another node (or none) refuses to start
// instead of serving a certificate the primary would reject.
func OwnCertCheck(nodeID string, now func() time.Time) func(*x509.Certificate) error {
	return func(c *x509.Certificate) error {
		t := time.Now()
		if now != nil {
			t = now()
		}
		switch {
		case c.IsCA || len(c.URIs) != 1 || c.URIs[0].String() != NodeURISAN(nodeID):
			return fmt.Errorf("helper certificate must carry exactly the URI SAN %s", NodeURISAN(nodeID))
		case !slices.Contains(c.ExtKeyUsage, x509.ExtKeyUsageServerAuth):
			return errors.New("helper certificate must have the serverAuth extended key usage")
		case t.Before(c.NotBefore) || t.After(c.NotAfter):
			return errors.New("helper certificate is not valid now")
		case c.NotAfter.Sub(c.NotBefore) > MaxCertLifetime+certBackdateSlack:
			return fmt.Errorf("helper certificate lives longer than %s", MaxCertLifetime)
		}
		return nil
	}
}

// KeyPair serves the listener's own certificate from files and picks up a
// renewed pair (certificates live at most 72h and are rotated in place) without
// a restart. A failed or non-conforming reload keeps serving the last good pair.
// (Same behaviour as helperauth.KeyPair; duplicated because helperauth imports
// the node registry and with it the Postgres driver.)
type KeyPair struct {
	certFile, keyFile string
	check             func(*x509.Certificate) error
	reloadEvery       time.Duration

	mu        sync.Mutex
	cert      *tls.Certificate
	certMod   time.Time
	keyMod    time.Time
	lastCheck time.Time
}

// NewKeyPair loads the pair once; a bad pair fails startup. check (optional)
// vets the certificate, at startup and on every reload.
func NewKeyPair(certFile, keyFile string, check func(*x509.Certificate) error) (*KeyPair, error) {
	k := &KeyPair{certFile: certFile, keyFile: keyFile, check: check, reloadEvery: 5 * time.Second}
	if err := k.load(); err != nil {
		return nil, err
	}
	k.certMod, k.keyMod = modTime(certFile), modTime(keyFile)
	return k, nil
}

func (k *KeyPair) load() error {
	cert, err := tls.LoadX509KeyPair(k.certFile, k.keyFile)
	if err != nil {
		return fmt.Errorf("load helper TLS key pair: %w", err)
	}
	if cert.Leaf == nil {
		if cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return fmt.Errorf("parse helper TLS certificate: %w", err)
		}
	}
	if k.check != nil {
		if err := k.check(cert.Leaf); err != nil {
			return err
		}
	}
	k.cert = &cert
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
		cm, km := modTime(k.certFile), modTime(k.keyFile)
		if !cm.Equal(k.certMod) || !km.Equal(k.keyMod) {
			k.certMod, k.keyMod = cm, km // retry on the next file change, not on every handshake
			if err := k.load(); err != nil {
				slog.Warn("helper TLS key pair reload failed; keeping the previous pair", "error", err)
			}
		}
	}
	return k.cert, nil
}
