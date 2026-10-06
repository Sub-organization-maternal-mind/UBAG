package appjwt

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	token, err := IssueToken("tenant1", "app1", "admin", time.Hour, priv)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if strings.Count(token, ".") != 2 {
		t.Errorf("token must have 3 parts: %q", token)
	}

	claims, err := Verify(token, &priv.PublicKey)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.TenantID != "tenant1" || claims.AppID != "app1" || claims.Role != "admin" {
		t.Errorf("claims mismatch: %+v", claims)
	}
}

func TestVerifyWrongKey(t *testing.T) {
	priv1, _ := GenerateKeyPair()
	priv2, _ := GenerateKeyPair()

	token, _ := IssueToken("t", "a", "viewer", time.Hour, priv1)
	if _, err := Verify(token, &priv2.PublicKey); err == nil {
		t.Error("Verify must fail with the wrong public key")
	}
}

func TestVerifyExpired(t *testing.T) {
	priv, _ := GenerateKeyPair()
	// Issue a token that expired 1 second ago.
	claims := AppClaims{
		TenantID: "t", AppID: "a", Role: "viewer",
		IssuedAt: time.Now().Add(-2 * time.Second).Unix(),
		Expires:  time.Now().Add(-time.Second).Unix(),
	}
	token, _ := Sign(claims, priv)
	_, err := Verify(token, &priv.PublicKey)
	if !errors.Is(err, ErrExpired) {
		t.Errorf("Verify must return ErrExpired for expired token, got: %v", err)
	}
}

func TestVerifyMalformed(t *testing.T) {
	priv, _ := GenerateKeyPair()
	for _, bad := range []string{"", "a", "a.b", "a.b.c.d"} {
		if _, err := Verify(bad, &priv.PublicKey); err == nil {
			t.Errorf("Verify(%q) must fail", bad)
		}
	}
}

func TestVerifyTamperedPayload(t *testing.T) {
	priv, _ := GenerateKeyPair()
	token, _ := IssueToken("t", "a", "viewer", time.Hour, priv)
	parts := strings.SplitN(token, ".", 3)
	// Replace payload with a different base64-encoded JSON
	parts[1] = base64url(mustJSON(AppClaims{TenantID: "evil", AppID: "evil", Role: "admin"}))
	tampered := strings.Join(parts, ".")
	if _, err := Verify(tampered, &priv.PublicKey); err == nil {
		t.Error("Verify must fail for tampered payload")
	}
}

func TestSignNilKey(t *testing.T) {
	if _, err := Sign(AppClaims{}, nil); err == nil {
		t.Error("Sign must fail with nil private key")
	}
}

func TestVerifyNilKey(t *testing.T) {
	priv, _ := GenerateKeyPair()
	token, _ := IssueToken("t", "a", "viewer", time.Hour, priv)
	if _, err := Verify(token, (*rsa.PublicKey)(nil)); err == nil {
		t.Error("Verify must fail with nil public key")
	}
}

func TestIsExpired(t *testing.T) {
	past := AppClaims{Expires: time.Now().Add(-time.Second).Unix()}
	if !past.IsExpired(time.Now()) {
		t.Error("IsExpired must return true for past expiry")
	}
	future := AppClaims{Expires: time.Now().Add(time.Hour).Unix()}
	if future.IsExpired(time.Now()) {
		t.Error("IsExpired must return false for future expiry")
	}
}

// signRaw signs an arbitrary header/payload with the app key, to model a token
// minted by something other than IssueToken.
func signRaw(t *testing.T, header, payload string, priv *rsa.PrivateKey) string {
	t.Helper()
	in := base64url([]byte(header)) + "." + base64url([]byte(payload))
	digest := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + base64url(sig)
}

func TestVerifyRejectsAudienceBoundAndAttemptTokens(t *testing.T) {
	priv, _ := GenerateKeyPair()
	exp := time.Now().Add(time.Hour).Unix()
	body := func(extra string) string {
		return `{"tid":"t","sub":"a","role":"admin","iat":1,"exp":` + strconv.FormatInt(exp, 10) + extra + `}`
	}
	if _, err := Verify(signRaw(t, `{"alg":"RS256","typ":"JWT"}`, body(""), priv), &priv.PublicKey); err != nil {
		t.Fatalf("baseline app token must verify: %v", err)
	}
	for name, tok := range map[string]string{
		"aud string":  signRaw(t, `{"alg":"RS256","typ":"JWT"}`, body(`,"aud":"ubag-helper"`), priv),
		"aud array":   signRaw(t, `{"alg":"RS256","typ":"JWT"}`, body(`,"aud":["x"]`), priv),
		"aud null":    signRaw(t, `{"alg":"RS256","typ":"JWT"}`, body(`,"aud":null`), priv),
		"attempt typ": signRaw(t, `{"alg":"RS256","typ":"ubag-attempt"}`, body(""), priv),
	} {
		if _, err := Verify(tok, &priv.PublicKey); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
}
