package sso

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

var (
	// ErrAssertionMalformed indicates the SAML XML could not be parsed.
	ErrAssertionMalformed = errors.New("sso: saml assertion is malformed")
	// ErrAssertionUnsigned indicates the assertion carried no <Signature>.
	ErrAssertionUnsigned = errors.New("sso: saml assertion is not signed")
	// ErrAssertionSignature indicates the digest or RSA signature did not verify.
	ErrAssertionSignature = errors.New("sso: saml assertion signature is invalid")
	// ErrAssertionExpired indicates now is outside the Conditions validity window.
	ErrAssertionExpired = errors.New("sso: saml assertion is expired (outside NotBefore/NotOnOrAfter)")
	// ErrAssertionNotYetValid indicates now is before Conditions.NotBefore.
	ErrAssertionNotYetValid = errors.New("sso: saml assertion is not yet valid")
	// ErrSAMLNoCert indicates the SAML config carried no usable IdP certificate.
	ErrSAMLNoCert = errors.New("sso: saml config has no IdP certificate")
)

// xmlAssertion mirrors the minimal SAML 2.0 assertion shape this package
// consumes. Element matching is by local name, so namespace prefixes (saml:,
// ds:, etc.) are accepted transparently.
type xmlAssertion struct {
	XMLName    xml.Name           `xml:"Assertion"`
	ID         string             `xml:"ID,attr"`
	Issuer     string             `xml:"Issuer"`
	NameID     string             `xml:"Subject>NameID"`
	Conditions xmlConditions      `xml:"Conditions"`
	Statements []xmlAttrStatement `xml:"AttributeStatement"`
	Signature  *xmlSignature      `xml:"Signature"`
}

type xmlConditions struct {
	NotBefore    string `xml:"NotBefore,attr"`
	NotOnOrAfter string `xml:"NotOnOrAfter,attr"`
}

type xmlAttrStatement struct {
	Attributes []xmlAttribute `xml:"Attribute"`
}

type xmlAttribute struct {
	Name   string   `xml:"Name,attr"`
	Values []string `xml:"AttributeValue"`
}

type xmlSignature struct {
	DigestValue    string `xml:"SignedInfo>Reference>DigestValue"`
	SignatureValue string `xml:"SignatureValue"`
}

// ParseAndVerifyAssertion parses a SAML 2.0 <Assertion> and verifies it against
// cfg, returning the extracted assertion data.
//
// Signature verification uses a pragmatic, clearly documented enveloped-signature
// scheme (a full XML-DSig exclusive-canonicalization implementation is out of
// scope for a stdlib-only package):
//
//   - The signed element is the <Assertion> with its nested <Signature> element
//     removed verbatim (enveloped-signature transform over raw bytes).
//   - DigestValue MUST equal base64(SHA-256(signed-element raw bytes)).
//   - SignatureValue MUST be a valid RSA-PKCS1v15/SHA-256 signature, made with
//     the IdP private key, over the raw bytes of the <SignedInfo> element. It is
//     verified here with the configured IdP PUBLIC certificate.
//
// An assertion without a <Signature> is always rejected. After signature
// verification the Conditions NotBefore / NotOnOrAfter window is enforced
// against now.
func ParseAndVerifyAssertion(ctx context.Context, xmlBytes []byte, cfg SAMLConfig, now time.Time) (Assertion, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return Assertion{}, err
		}
	}
	now = now.UTC()

	var parsed xmlAssertion
	if err := xml.Unmarshal(xmlBytes, &parsed); err != nil {
		return Assertion{}, fmt.Errorf("%w: %v", ErrAssertionMalformed, err)
	}
	if parsed.Signature == nil {
		return Assertion{}, ErrAssertionUnsigned
	}

	pub, err := parseRSACertPublicKeyPEM(cfg.IdPCertPEM)
	if err != nil {
		return Assertion{}, err
	}

	if err := verifyEnvelopedSignature(xmlBytes, parsed.Signature, pub); err != nil {
		return Assertion{}, err
	}

	notBefore, err := parseSAMLTime(parsed.Conditions.NotBefore)
	if err != nil {
		return Assertion{}, fmt.Errorf("%w: NotBefore: %v", ErrAssertionMalformed, err)
	}
	notOnOrAfter, err := parseSAMLTime(parsed.Conditions.NotOnOrAfter)
	if err != nil {
		return Assertion{}, fmt.Errorf("%w: NotOnOrAfter: %v", ErrAssertionMalformed, err)
	}
	if !notBefore.IsZero() && now.Add(clockSkew).Before(notBefore) {
		return Assertion{}, ErrAssertionNotYetValid
	}
	if !notOnOrAfter.IsZero() && !now.Add(-clockSkew).Before(notOnOrAfter) {
		return Assertion{}, ErrAssertionExpired
	}

	attributes := map[string][]string{}
	for _, statement := range parsed.Statements {
		for _, attribute := range statement.Attributes {
			values := make([]string, 0, len(attribute.Values))
			for _, value := range attribute.Values {
				values = append(values, strings.TrimSpace(value))
			}
			attributes[attribute.Name] = append(attributes[attribute.Name], values...)
		}
	}

	return Assertion{
		Issuer:       strings.TrimSpace(parsed.Issuer),
		Subject:      strings.TrimSpace(parsed.NameID),
		NotBefore:    notBefore,
		NotOnOrAfter: notOnOrAfter,
		Attributes:   attributes,
	}, nil
}

func verifyEnvelopedSignature(xmlBytes []byte, signature *xmlSignature, pub *rsa.PublicKey) error {
	signedInfoStart, signedInfoEnd, ok, err := elementRange(xmlBytes, "SignedInfo")
	if err != nil || !ok {
		return fmt.Errorf("%w: SignedInfo element not found", ErrAssertionSignature)
	}
	signedInfoBytes := xmlBytes[signedInfoStart:signedInfoEnd]

	signatureValue, err := base64.StdEncoding.DecodeString(stripWhitespace(signature.SignatureValue))
	if err != nil {
		return fmt.Errorf("%w: bad SignatureValue base64: %v", ErrAssertionSignature, err)
	}
	signedInfoDigest := sha256.Sum256(signedInfoBytes)
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, signedInfoDigest[:], signatureValue); err != nil {
		return fmt.Errorf("%w: SignedInfo signature: %v", ErrAssertionSignature, err)
	}

	expectedDigest, err := base64.StdEncoding.DecodeString(stripWhitespace(signature.DigestValue))
	if err != nil {
		return fmt.Errorf("%w: bad DigestValue base64: %v", ErrAssertionSignature, err)
	}
	signedElement, err := assertionBytesWithoutSignature(xmlBytes)
	if err != nil {
		return err
	}
	actualDigest := sha256.Sum256(signedElement)
	if !bytes.Equal(expectedDigest, actualDigest[:]) {
		return fmt.Errorf("%w: digest mismatch over signed element", ErrAssertionSignature)
	}
	return nil
}

// assertionBytesWithoutSignature returns the raw bytes of the <Assertion>
// element with its nested <Signature> element removed verbatim, implementing
// the enveloped-signature transform over the original byte stream.
func assertionBytesWithoutSignature(xmlBytes []byte) ([]byte, error) {
	assertionStart, assertionEnd, ok, err := elementRange(xmlBytes, "Assertion")
	if err != nil || !ok {
		return nil, fmt.Errorf("%w: Assertion element not found", ErrAssertionSignature)
	}
	signatureStart, signatureEnd, ok, err := elementRange(xmlBytes, "Signature")
	if err != nil || !ok {
		return nil, fmt.Errorf("%w: Signature element not found", ErrAssertionSignature)
	}
	if signatureStart < assertionStart || signatureEnd > assertionEnd {
		return nil, fmt.Errorf("%w: Signature is not enveloped within Assertion", ErrAssertionSignature)
	}
	assertion := xmlBytes[assertionStart:assertionEnd]
	relativeStart := signatureStart - assertionStart
	relativeEnd := signatureEnd - assertionStart
	out := make([]byte, 0, len(assertion)-(relativeEnd-relativeStart))
	out = append(out, assertion[:relativeStart]...)
	out = append(out, assertion[relativeEnd:]...)
	return out, nil
}

// elementRange returns the absolute byte range [start, end) of the first
// element whose local name matches localName, including its start and end tags.
func elementRange(data []byte, localName string) (int, int, bool, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	start := 0
	found := false
	for {
		offsetBefore := decoder.InputOffset()
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, 0, false, err
		}
		switch element := token.(type) {
		case xml.StartElement:
			if !found && element.Name.Local == localName {
				start = indexOfOpeningAngle(data, int(offsetBefore))
				found = true
				depth = 1
			} else if found {
				depth++
			}
		case xml.EndElement:
			if found {
				depth--
				if depth == 0 {
					return start, int(decoder.InputOffset()), true, nil
				}
			}
		}
	}
	return 0, 0, false, nil
}

// indexOfOpeningAngle advances from offset to the next '<' so that the returned
// start index points precisely at the element's opening tag, skipping any
// inter-token whitespace the decoder may have positioned us before.
func indexOfOpeningAngle(data []byte, offset int) int {
	for index := offset; index < len(data); index++ {
		if data[index] == '<' {
			return index
		}
	}
	return offset
}

func parseRSACertPublicKeyPEM(pemStr string) (*rsa.PublicKey, error) {
	if strings.TrimSpace(pemStr) == "" {
		return nil, ErrSAMLNoCert
	}
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("%w: invalid PEM", ErrSAMLNoCert)
	}
	switch block.Type {
	case "CERTIFICATE":
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		pub, ok := cert.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("sso: IdP certificate does not contain an RSA public key")
		}
		return pub, nil
	case "PUBLIC KEY":
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		pub, ok := parsed.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("sso: IdP public key is not RSA")
		}
		return pub, nil
	default:
		return nil, fmt.Errorf("sso: unsupported IdP PEM block type %q", block.Type)
	}
}

func parseSAMLTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp %q", value)
}

func stripWhitespace(value string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		default:
			return r
		}
	}, value)
}
