package security

import "testing"

func TestHashAndVerify(t *testing.T) {
	tok := GenerateToken()
	if len(tok) < 40 {
		t.Fatalf("token too short: %q", tok)
	}
	h := HashSecret(tok)
	if h == tok {
		t.Fatal("hash must differ from secret")
	}
	if !VerifySecret(tok, h) {
		t.Fatal("expected verify to succeed")
	}
	if VerifySecret("wrong", h) {
		t.Fatal("expected verify to fail for wrong secret")
	}
}

func TestTokensAreUnique(t *testing.T) {
	if GenerateToken() == GenerateToken() {
		t.Fatal("tokens must be unique")
	}
}
