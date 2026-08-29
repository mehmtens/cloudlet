package config

import "testing"

func setProductionEnv(t *testing.T, cors string) {
	t.Helper()
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://user:pass@db/cloudlet")
	t.Setenv("JWT_SECRET", "01234567890123456789012345678901")
	t.Setenv("PUBLIC_BASE_URL", "https://cloudlet.example.com")
	t.Setenv("COOKIE_SECURE", "true")
	t.Setenv("S3_BUCKET", "cloudlet")
	t.Setenv("S3_CORS_ORIGIN", cors)
	t.Setenv("S3_SERVER_SIDE_ENCRYPTION", "AES256")
	t.Setenv("SMTP_ADDRESS", "smtp.example.com:587")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_USERNAME", "mailer")
	t.Setenv("SMTP_PASSWORD", "secret")
	t.Setenv("SMTP_FROM", "Cloudlet <no-reply@example.com>")
	t.Setenv("SMTP_REQUIRE_TLS", "true")
	t.Setenv("CLAMAV_ADDRESS", "clamav.internal:3310")
	t.Setenv("TOTP_ENCRYPTION_KEY", "MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=")
}

func TestLoadRejectsNonHTTPSProductionCORS(t *testing.T) {
	setProductionEnv(t, "http://cloudlet.example.com")
	if _, err := Load(); err == nil {
		t.Fatal("expected non-HTTPS CORS origin to be rejected")
	}
}

func TestLoadAcceptsHTTPSProductionCORS(t *testing.T) {
	setProductionEnv(t, "https://cloudlet.example.com")
	if _, err := Load(); err != nil {
		t.Fatalf("expected valid production configuration: %v", err)
	}
}

func TestLoadRejectsInvalidS3Encryption(t *testing.T) {
	setProductionEnv(t, "https://cloudlet.example.com")
	t.Setenv("S3_SERVER_SIDE_ENCRYPTION", "none")
	if _, err := Load(); err == nil {
		t.Fatal("expected unsupported S3 encryption to be rejected")
	}
}
