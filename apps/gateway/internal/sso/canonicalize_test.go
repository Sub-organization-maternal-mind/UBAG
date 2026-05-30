package sso

import "testing"

func TestExclusiveCanonicalize_SortsAttributesAndExpandsEmptyElements(t *testing.T) {
	in := []byte(`<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" IssueInstant="t" ID="_a1"><saml:Empty/></saml:Assertion>`)
	got, err := exclusiveCanonicalize(in)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	want := `<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="_a1" IssueInstant="t"><saml:Empty></saml:Empty></saml:Assertion>`
	if string(got) != want {
		t.Fatalf("canonical form mismatch:\n got=%s\nwant=%s", got, want)
	}
}

func TestExclusiveCanonicalize_SuppressesRedundantNamespaceAndEscapesText(t *testing.T) {
	in := []byte(`<saml:Assertion xmlns:saml="urn:x"><saml:Issuer>a &amp; b &lt; c</saml:Issuer></saml:Assertion>`)
	got, err := exclusiveCanonicalize(in)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	// xmlns:saml is rendered once on the root and not repeated on the child.
	want := `<saml:Assertion xmlns:saml="urn:x"><saml:Issuer>a &amp; b &lt; c</saml:Issuer></saml:Assertion>`
	if string(got) != want {
		t.Fatalf("canonical form mismatch:\n got=%s\nwant=%s", got, want)
	}
}

func TestExclusiveCanonicalize_DeterministicForEquivalentInput(t *testing.T) {
	a := []byte(`<r xmlns="urn:x" b="2" a="1"><c/></r>`)
	b := []byte(`<r a="1" b="2" xmlns="urn:x"><c></c></r>`)
	canonA, err := exclusiveCanonicalize(a)
	if err != nil {
		t.Fatalf("canonicalize a: %v", err)
	}
	canonB, err := exclusiveCanonicalize(b)
	if err != nil {
		t.Fatalf("canonicalize b: %v", err)
	}
	if string(canonA) != string(canonB) {
		t.Fatalf("equivalent documents produced different canonical forms:\n a=%s\n b=%s", canonA, canonB)
	}
}
