package serve

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/appjwt"
)

func pkixPublicKeyPEM(t *testing.T) (string, string) {
	t.Helper()
	priv, err := appjwt.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	return pemText, priv.PublicKey.N.String()
}

func TestAppJWTPublicKeyFromEnvUnset(t *testing.T) {
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY", "")
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY_FILE", "")
	key, err := appJWTPublicKeyFromEnv()
	if err != nil {
		t.Fatalf("unset env: unexpected error %v", err)
	}
	if key != nil {
		t.Fatal("unset env: expected nil key (feature disabled)")
	}
}

func TestAppJWTPublicKeyFromEnvInlinePEM(t *testing.T) {
	pemText, modulus := pkixPublicKeyPEM(t)
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY", pemText)
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY_FILE", "")
	key, err := appJWTPublicKeyFromEnv()
	if err != nil {
		t.Fatalf("inline PEM: %v", err)
	}
	if key == nil || key.N.String() != modulus {
		t.Fatal("inline PEM: parsed key does not match")
	}
}

func TestAppJWTPublicKeyFromEnvInlinePEMWithEscapedNewlines(t *testing.T) {
	// docker-compose / .env files often carry PEM as a single line with literal
	// \n sequences; the loader must accept that form.
	pemText, modulus := pkixPublicKeyPEM(t)
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY", strings.ReplaceAll(pemText, "\n", `\n`))
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY_FILE", "")
	key, err := appJWTPublicKeyFromEnv()
	if err != nil {
		t.Fatalf("escaped-newline PEM: %v", err)
	}
	if key == nil || key.N.String() != modulus {
		t.Fatal("escaped-newline PEM: parsed key does not match")
	}
}

func TestAppJWTPublicKeyFromEnvFile(t *testing.T) {
	pemText, modulus := pkixPublicKeyPEM(t)
	path := filepath.Join(t.TempDir(), "appjwt-public.pem")
	if err := os.WriteFile(path, []byte(pemText), 0o600); err != nil {
		t.Fatalf("write pem file: %v", err)
	}
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY", "")
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY_FILE", path)
	key, err := appJWTPublicKeyFromEnv()
	if err != nil {
		t.Fatalf("file PEM: %v", err)
	}
	if key == nil || key.N.String() != modulus {
		t.Fatal("file PEM: parsed key does not match")
	}
}

func TestAppJWTPublicKeyFromEnvInlineTakesPrecedenceOverFile(t *testing.T) {
	pemText, modulus := pkixPublicKeyPEM(t)
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY", pemText)
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY_FILE", filepath.Join(t.TempDir(), "does-not-exist.pem"))
	key, err := appJWTPublicKeyFromEnv()
	if err != nil {
		t.Fatalf("inline precedence: %v", err)
	}
	if key == nil || key.N.String() != modulus {
		t.Fatal("inline precedence: parsed key does not match")
	}
}

func TestAppJWTPublicKeyFromEnvPKCS1(t *testing.T) {
	priv, err := appjwt.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&priv.PublicKey)}))
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY", pemText)
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY_FILE", "")
	key, err := appJWTPublicKeyFromEnv()
	if err != nil {
		t.Fatalf("PKCS1 PEM: %v", err)
	}
	if key == nil || key.N.String() != priv.PublicKey.N.String() {
		t.Fatal("PKCS1 PEM: parsed key does not match")
	}
}

func TestAppJWTPublicKeyFromEnvMalformed(t *testing.T) {
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY", "not a pem at all")
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY_FILE", "")
	if _, err := appJWTPublicKeyFromEnv(); err == nil {
		t.Fatal("malformed PEM: expected startup error, got nil")
	}
}

func TestAppJWTPublicKeyFromEnvMissingFile(t *testing.T) {
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY", "")
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY_FILE", filepath.Join(t.TempDir(), "missing.pem"))
	if _, err := appJWTPublicKeyFromEnv(); err == nil {
		t.Fatal("missing file: expected startup error, got nil")
	}
}

func TestAppJWTPublicKeyFromEnvRejectsNonRSAKey(t *testing.T) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&ecKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal ecdsa public key: %v", err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY", pemText)
	t.Setenv("UBAG_APP_JWT_PUBLIC_KEY_FILE", "")
	if _, err := appJWTPublicKeyFromEnv(); err == nil {
		t.Fatal("non-RSA key: expected startup error, got nil")
	}
}
