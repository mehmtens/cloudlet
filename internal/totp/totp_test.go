package totp

import (
	"testing"
	"time"
)

func TestGenerateAndValidate(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1710000000, 0)
	code, err := Code(secret, at)
	if err != nil {
		t.Fatal(err)
	}
	if !Validate(secret, code, at) {
		t.Fatal("expected current code to validate")
	}
	if !Validate(secret, code, at.Add(30*time.Second)) {
		t.Fatal("expected adjacent time step tolerance")
	}
	if Validate(secret, "000000", at) {
		t.Fatal("unexpected invalid code acceptance")
	}
}
