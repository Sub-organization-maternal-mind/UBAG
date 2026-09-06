package apikey

import (
	"strings"
	"testing"
)

func TestGenerateValidateRoundTrip(t *testing.T) {
	k, err := Generate("prod")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(k.Formatted, "ubag_sk_prod_") {
		t.Errorf("Formatted = %q, want ubag_sk_prod_ prefix", k.Formatted)
	}
	if len(k.Raw) != rawKeyLen {
		t.Errorf("Raw len = %d, want %d", len(k.Raw), rawKeyLen)
	}

	got, err := Validate(k.Formatted)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.Env != "prod" {
		t.Errorf("Env = %q, want prod", got.Env)
	}
	if string(got.Raw) != string(k.Raw) {
		t.Error("Raw bytes differ after validate round-trip")
	}
}

func TestGenerateDevEnv(t *testing.T) {
	k, err := Generate("dev")
	if err != nil {
		t.Fatalf("Generate dev: %v", err)
	}
	if _, err := Validate(k.Formatted); err != nil {
		t.Fatalf("Validate dev key: %v", err)
	}
}

func TestGenerateKeysAreUnique(t *testing.T) {
	k1, _ := Generate("prod")
	k2, _ := Generate("prod")
	if k1.Formatted == k2.Formatted {
		t.Error("two generated keys must be distinct")
	}
}

func TestValidateInvalidPrefix(t *testing.T) {
	if _, err := Validate("sk_prod_something"); err == nil {
		t.Error("expected error for missing ubag_sk_ prefix")
	}
}

func TestValidateNoEnv(t *testing.T) {
	if _, err := Validate("ubag_sk_payload"); err == nil {
		t.Error("expected error for missing env segment")
	}
}

func TestValidateChecksumMismatch(t *testing.T) {
	k, _ := Generate("prod")
	// Flip the last character of the base58 payload.
	chars := []rune(k.Formatted)
	if chars[len(chars)-1] == 'z' {
		chars[len(chars)-1] = 'a'
	} else {
		chars[len(chars)-1] = 'z'
	}
	tampered := string(chars)
	_, err := Validate(tampered)
	if err == nil {
		t.Error("expected error for tampered key")
	}
}

func TestValidateInvalidEnvChar(t *testing.T) {
	if _, err := Validate("ubag_sk_PROD_payload"); err == nil {
		t.Error("expected error for uppercase env")
	}
}

func TestGenerateInvalidEnv(t *testing.T) {
	if _, err := Generate("PROD"); err == nil {
		t.Error("expected error for uppercase env in Generate")
	}
	if _, err := Generate(""); err == nil {
		t.Error("expected error for empty env in Generate")
	}
	if _, err := Generate("1env"); err == nil {
		t.Error("expected error for env starting with digit")
	}
}

func TestBase58RoundTrip(t *testing.T) {
	data := []byte("hello world from ubag")
	encoded := base58Encode(data)
	decoded, err := base58Decode(encoded)
	if err != nil {
		t.Fatalf("base58Decode: %v", err)
	}
	if string(decoded) != string(data) {
		t.Errorf("got %q, want %q", decoded, data)
	}
}

func TestBase58InvalidChar(t *testing.T) {
	if _, err := base58Decode("0OIl!"); err == nil {
		t.Error("expected error for invalid base58 characters")
	}
}
