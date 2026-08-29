package config

import (
	"encoding/base64"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mehmtens/cloudlet/internal/storage"
)

type Config struct {
	AppEnv            string
	HTTPAddr          string
	DatabaseURL       string
	MaxUploadBytes    int64
	JWTSecret         string
	JWTKID            string
	JWTPreviousSecret string
	JWTPreviousKID    string
	PublicBaseURL     string
	CookieSecure      bool
	PDFToPPMPath      string
	SMTPAddress       string
	SMTPHost          string
	SMTPUsername      string
	SMTPPassword      string
	SMTPFrom          string
	SMTPRequireTLS    bool
	ClamAVAddress     string
	ClamAVTimeout     time.Duration
	TrustedProxyCIDRs []string
	TOTPEncryptionKey []byte
	S3                storage.Config
}

func Load() (Config, error) {
	maxUploadBytes, err := strconv.ParseInt(env("MAX_UPLOAD_BYTES", "104857600"), 10, 64)
	if err != nil || maxUploadBytes <= 0 {
		return Config{}, fmt.Errorf("MAX_UPLOAD_BYTES must be a positive integer")
	}
	clamAVTimeout, err := time.ParseDuration(env("CLAMAV_SCAN_TIMEOUT", "2m"))
	if err != nil || clamAVTimeout <= 0 {
		return Config{}, fmt.Errorf("CLAMAV_SCAN_TIMEOUT must be a positive duration")
	}
	var totpEncryptionKey []byte
	if encoded := os.Getenv("TOTP_ENCRYPTION_KEY"); encoded != "" {
		totpEncryptionKey, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(totpEncryptionKey) != 32 {
			return Config{}, fmt.Errorf("TOTP_ENCRYPTION_KEY must be base64-encoded 32 bytes")
		}
	}
	cfg := Config{
		AppEnv: env("APP_ENV", "development"), HTTPAddr: env("HTTP_ADDR", ":8080"), DatabaseURL: os.Getenv("DATABASE_URL"), MaxUploadBytes: maxUploadBytes,
		JWTSecret:         os.Getenv("JWT_SECRET"),
		JWTKID:            env("JWT_KID", "v1"),
		JWTPreviousSecret: os.Getenv("JWT_PREVIOUS_SECRET"),
		JWTPreviousKID:    os.Getenv("JWT_PREVIOUS_KID"),
		PublicBaseURL:     env("PUBLIC_BASE_URL", "http://localhost:18080"),
		CookieSecure:      envBool("COOKIE_SECURE", false),
		PDFToPPMPath:      env("PDFTOPPM_PATH", "pdftoppm"),
		SMTPAddress:       env("SMTP_ADDRESS", "localhost:1025"),
		SMTPHost:          env("SMTP_HOST", "localhost"),
		SMTPUsername:      os.Getenv("SMTP_USERNAME"),
		SMTPPassword:      os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:          env("SMTP_FROM", "Cloudlet <no-reply@cloudlet.local>"),
		SMTPRequireTLS:    envBool("SMTP_REQUIRE_TLS", false),
		ClamAVAddress:     os.Getenv("CLAMAV_ADDRESS"),
		ClamAVTimeout:     clamAVTimeout,
		TrustedProxyCIDRs: splitCSV(os.Getenv("TRUSTED_PROXY_CIDRS")),
		TOTPEncryptionKey: totpEncryptionKey,
		S3: storage.Config{Endpoint: os.Getenv("S3_ENDPOINT"), PublicEndpoint: os.Getenv("S3_PUBLIC_ENDPOINT"), CORSOrigin: env("S3_CORS_ORIGIN", "*"), Region: env("S3_REGION", "us-east-1"),
			Bucket: os.Getenv("S3_BUCKET"), AccessKey: os.Getenv("S3_ACCESS_KEY"), SecretKey: os.Getenv("S3_SECRET_KEY"),
			ServerSideEncryption: os.Getenv("S3_SERVER_SIDE_ENCRYPTION"), KMSKeyID: os.Getenv("S3_KMS_KEY_ID"), UsePathStyle: envBool("S3_USE_PATH_STYLE", false)},
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.S3.Bucket == "" {
		return Config{}, fmt.Errorf("S3_BUCKET is required")
	}
	if len(cfg.JWTSecret) < 32 {
		return Config{}, fmt.Errorf("JWT_SECRET must be at least 32 characters")
	}
	if cfg.JWTPreviousSecret != "" && (len(cfg.JWTPreviousSecret) < 32 || cfg.JWTPreviousKID == "") {
		return Config{}, fmt.Errorf("JWT_PREVIOUS_SECRET requires a 32-character secret and JWT_PREVIOUS_KID")
	}
	if cfg.S3.ServerSideEncryption != "" && cfg.S3.ServerSideEncryption != "AES256" && cfg.S3.ServerSideEncryption != "aws:kms" {
		return Config{}, fmt.Errorf("S3_SERVER_SIDE_ENCRYPTION must be AES256 or aws:kms")
	}
	if cfg.S3.KMSKeyID != "" && cfg.S3.ServerSideEncryption != "aws:kms" {
		return Config{}, fmt.Errorf("S3_KMS_KEY_ID requires S3_SERVER_SIDE_ENCRYPTION=aws:kms")
	}
	for _, cidr := range cfg.TrustedProxyCIDRs {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return Config{}, fmt.Errorf("TRUSTED_PROXY_CIDRS contains an invalid CIDR: %s", cidr)
		}
	}
	if cfg.AppEnv == "production" {
		if len(cfg.TOTPEncryptionKey) != 32 {
			return Config{}, fmt.Errorf("TOTP_ENCRYPTION_KEY is required in production")
		}
		if cfg.ClamAVAddress == "" {
			return Config{}, fmt.Errorf("CLAMAV_ADDRESS is required in production")
		}
		if cfg.S3.ServerSideEncryption == "" {
			return Config{}, fmt.Errorf("S3_SERVER_SIDE_ENCRYPTION is required in production")
		}
		if cfg.S3.CORSOrigin == "" || cfg.S3.CORSOrigin == "*" {
			return Config{}, fmt.Errorf("S3_CORS_ORIGIN must be a specific HTTPS origin in production")
		}
		corsURL, err := url.Parse(cfg.S3.CORSOrigin)
		if err != nil || corsURL.Scheme != "https" || corsURL.Host == "" || corsURL.Path != "" || corsURL.RawQuery != "" || corsURL.Fragment != "" {
			return Config{}, fmt.Errorf("S3_CORS_ORIGIN must be a specific HTTPS origin in production")
		}
		publicURL, err := url.Parse(cfg.PublicBaseURL)
		if err != nil || publicURL.Scheme != "https" || publicURL.Host == "" {
			return Config{}, fmt.Errorf("PUBLIC_BASE_URL must be an absolute HTTPS URL in production")
		}
		if !cfg.CookieSecure {
			return Config{}, fmt.Errorf("COOKIE_SECURE must be true in production")
		}
		invalidSMTP := cfg.SMTPUsername == "" || cfg.SMTPPassword == "" || cfg.SMTPFrom == "" ||
			strings.Contains(strings.ToLower(cfg.SMTPAddress), "localhost") || strings.Contains(strings.ToLower(cfg.SMTPAddress), "mailpit") ||
			strings.HasSuffix(strings.ToLower(cfg.SMTPFrom), "@cloudlet.local")
		if invalidSMTP {
			return Config{}, fmt.Errorf("production requires real SMTP credentials and a verified SMTP_FROM address")
		}
		if !cfg.SMTPRequireTLS {
			return Config{}, fmt.Errorf("SMTP_REQUIRE_TLS must be true in production")
		}
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func splitCSV(value string) []string {
	result := []string{}
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
