package sso

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

// testKeypair generates an in-test RSA key and returns the private key plus the
// PEM-encoded PUBLIC key and a self-signed PUBLIC certificate (for SAML).
func testKeypair(t *testing.T) (*rsa.PrivateKey, string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-idp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
	return key, pubPEM, certPEM
}

// signJWT builds a compact JWT from the supplied header and claims, signing it
// with key. When the header has no alg the test must set it explicitly.
func signJWT(t *testing.T, key *rsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	headerSeg := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsSeg := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerSeg + "." + claimsSeg
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// buildSignedAssertion builds a SAML assertion and signs it using the pragmatic
// enveloped-signature scheme implemented by ParseAndVerifyAssertion. When sign
// is false the returned XML has no <Signature> element.
func buildSignedAssertion(t *testing.T, key *rsa.PrivateKey, notBefore, notOnOrAfter time.Time, sign bool) []byte {
	t.Helper()
	const closing = `</saml:Assertion>`
	assertionNoSig := `<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="_a1" IssueInstant="2026-01-01T00:00:00Z">` +
		`<saml:Issuer>https://idp.example/</saml:Issuer>` +
		`<saml:Subject><saml:NameID>operator@example.com</saml:NameID></saml:Subject>` +
		`<saml:Conditions NotBefore="` + notBefore.UTC().Format(time.RFC3339) + `" NotOnOrAfter="` + notOnOrAfter.UTC().Format(time.RFC3339) + `"></saml:Conditions>` +
		`<saml:AttributeStatement>` +
		`<saml:Attribute Name="tenant"><saml:AttributeValue>tenant-1</saml:AttributeValue></saml:Attribute>` +
		`<saml:Attribute Name="app"><saml:AttributeValue>app-1</saml:AttributeValue></saml:Attribute>` +
		`<saml:Attribute Name="role"><saml:AttributeValue>idp-admin</saml:AttributeValue></saml:Attribute>` +
		`</saml:AttributeStatement>` +
		closing

	if !sign {
		return []byte(assertionNoSig)
	}

	digest := sha256.Sum256([]byte(assertionNoSig))
	digestB64 := base64.StdEncoding.EncodeToString(digest[:])

	signedInfo := `<ds:SignedInfo xmlns:ds="http://www.w3.org/2000/09/xmldsig#">` +
		`<ds:CanonicalizationMethod Algorithm="http://www.w3.org/2001/10/xml-exc-c14n#"></ds:CanonicalizationMethod>` +
		`<ds:SignatureMethod Algorithm="http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"></ds:SignatureMethod>` +
		`<ds:Reference URI="#_a1">` +
		`<ds:DigestMethod Algorithm="http://www.w3.org/2001/04/xmlenc#sha256"></ds:DigestMethod>` +
		`<ds:DigestValue>` + digestB64 + `</ds:DigestValue>` +
		`</ds:Reference>` +
		`</ds:SignedInfo>`

	signedInfoDigest := sha256.Sum256([]byte(signedInfo))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, signedInfoDigest[:])
	if err != nil {
		t.Fatalf("sign SignedInfo: %v", err)
	}
	signatureB64 := base64.StdEncoding.EncodeToString(signature)

	signatureElement := `<ds:Signature xmlns:ds="http://www.w3.org/2000/09/xmldsig#">` +
		signedInfo +
		`<ds:SignatureValue>` + signatureB64 + `</ds:SignatureValue>` +
		`</ds:Signature>`

	signed := strings.Replace(assertionNoSig, closing, signatureElement+closing, 1)
	return []byte(signed)
}

// jwksFor builds a JWKS JSON document containing the public part of key.
func jwksFor(t *testing.T, key *rsa.PrivateKey, kid string) []byte {
	t.Helper()
	eBytes := big.NewInt(int64(key.PublicKey.E)).Bytes()
	doc := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"kid": kid,
				"n":   base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(eBytes),
			},
		},
	}
	blob, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	return blob
}
