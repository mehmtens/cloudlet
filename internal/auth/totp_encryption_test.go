package auth

import (
	"testing"

	"github.com/google/uuid"
)

func TestTOTPSecretEncryptionRoundTripAndUserBinding(t *testing.T) {
	service := &Service{}
	if err := service.ConfigureTOTPEncryption([]byte("01234567890123456789012345678901")); err != nil {
		t.Fatal(err)
	}
	userID := uuid.New()
	stored, err := service.encryptTOTPSecret(userID, "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	if stored == "JBSWY3DPEHPK3PXP" {
		t.Fatal("TOTP secret was stored as plaintext")
	}
	plain, err := service.decryptTOTPSecret(userID, stored)
	if err != nil || plain != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("decryptTOTPSecret() = %q, %v", plain, err)
	}
	if _, err := service.decryptTOTPSecret(uuid.New(), stored); err == nil {
		t.Fatal("ciphertext was accepted for a different user")
	}
}
