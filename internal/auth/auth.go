package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidCredentials = errors.New("invalid credentials")
var ErrEmailTaken = errors.New("email already exists")
var ErrInvalidEmail = errors.New("invalid email")
var ErrWeakPassword = errors.New("password must be at least 12 characters")

type User struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) Create(ctx context.Context, user User, passwordHash string) error {
	_, err := r.db.Exec(ctx, `INSERT INTO users (id, email, password_hash, created_at) VALUES ($1, $2, $3, $4)`,
		user.ID, user.Email, passwordHash, user.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrEmailTaken
	}
	return err
}

func (r *Repository) CredentialsByEmail(ctx context.Context, email string) (User, string, error) {
	var user User
	var passwordHash string
	err := r.db.QueryRow(ctx, `SELECT id, email, created_at, password_hash FROM users WHERE email = $1`, email).
		Scan(&user.ID, &user.Email, &user.CreatedAt, &passwordHash)
	return user, passwordHash, err
}

func (r *Repository) CredentialsByID(ctx context.Context, id uuid.UUID) (User, string, error) {
	var user User
	var passwordHash string
	err := r.db.QueryRow(ctx, `SELECT id, email, created_at, password_hash FROM users WHERE id = $1`, id).
		Scan(&user.ID, &user.Email, &user.CreatedAt, &passwordHash)
	return user, passwordHash, err
}

func (r *Repository) UpdatePassword(ctx context.Context, id uuid.UUID, passwordHash string) error {
	tag, err := r.db.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, id, passwordHash)
	if err == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}

type Service struct {
	repo           userRepository
	secret         []byte
	kid            string
	previousSecret []byte
	previousKID    string
	tokenTTL       time.Duration
	totpKey        []byte
}

func (s *Service) ConfigureTOTPEncryption(key []byte) error {
	if len(key) != 32 {
		return errors.New("TOTP encryption key must be 32 bytes")
	}
	s.totpKey = append([]byte(nil), key...)
	return nil
}

type userRepository interface {
	Create(context.Context, User, string) error
	CredentialsByEmail(context.Context, string) (User, string, error)
}

type passwordRepository interface {
	CredentialsByID(context.Context, uuid.UUID) (User, string, error)
	UpdatePassword(context.Context, uuid.UUID, string) error
}

func NewService(repo userRepository, secret string, tokenTTL time.Duration) *Service {
	return &Service{repo: repo, secret: []byte(secret), kid: "v1", tokenTTL: tokenTTL}
}

func NewServiceWithKeyRotation(repo userRepository, activeKID, activeSecret, previousKID, previousSecret string, tokenTTL time.Duration) *Service {
	return &Service{repo: repo, secret: []byte(activeSecret), kid: activeKID, previousSecret: []byte(previousSecret), previousKID: previousKID, tokenTTL: tokenTTL}
}

func (s *Service) Register(ctx context.Context, email, password string) (User, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		return User{}, "", ErrInvalidEmail
	}
	if len(password) < 12 {
		return User{}, "", ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, "", err
	}
	user := User{ID: uuid.New(), Email: email, CreatedAt: time.Now().UTC()}
	if err := s.repo.Create(ctx, user, string(hash)); err != nil {
		return User{}, "", err
	}
	token, err := s.issueToken(user)
	return user, token, err
}

func (s *Service) Login(ctx context.Context, email, password string) (User, string, error) {
	user, hash, err := s.repo.CredentialsByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", ErrInvalidCredentials
	}
	if err != nil {
		return User{}, "", err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return User{}, "", ErrInvalidCredentials
	}
	token, err := s.issueToken(user)
	return user, token, err
}

// ChangePassword verifies the current password, stores a new bcrypt hash, and
// invalidates all refresh sessions so previously issued sessions cannot persist.
func (s *Service) ChangePassword(ctx context.Context, userID uuid.UUID, currentPassword, newPassword string) error {
	if len(newPassword) < 12 {
		return ErrWeakPassword
	}
	repo, ok := s.repo.(passwordRepository)
	if !ok {
		return errors.New("password change is unavailable")
	}
	_, hash, err := repo.CredentialsByID(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && bcrypt.CompareHashAndPassword([]byte(hash), []byte(currentPassword)) != nil) {
		return ErrInvalidCredentials
	}
	if err != nil {
		return err
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := repo.UpdatePassword(ctx, userID, string(newHash)); err != nil {
		return err
	}
	if sessions, ok := s.repo.(sessionRepository); ok {
		if err := sessions.RevokeAllSessions(ctx, userID, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) VerifyPassword(ctx context.Context, userID uuid.UUID, password string) error {
	repo, ok := s.repo.(passwordRepository)
	if !ok {
		return errors.New("password verification is unavailable")
	}
	_, hash, err := repo.CredentialsByID(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil) {
		return ErrInvalidCredentials
	}
	return err
}

func (s *Service) ParseToken(raw string) (uuid.UUID, error) {
	token, err := jwt.Parse(raw, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		kid, _ := token.Header["kid"].(string)
		if kid == s.kid {
			return s.secret, nil
		}
		if kid == s.previousKID && len(s.previousSecret) > 0 {
			return s.previousSecret, nil
		}
		return nil, fmt.Errorf("unknown signing key")
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !token.Valid {
		return uuid.Nil, ErrInvalidCredentials
	}
	subject, err := token.Claims.GetSubject()
	if err != nil {
		return uuid.Nil, ErrInvalidCredentials
	}
	return uuid.Parse(subject)
}

func (s *Service) issueToken(user User) (string, error) {
	now := time.Now().UTC()
	claims := jwt.RegisteredClaims{Subject: user.ID.String(), IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(s.tokenTTL))}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["kid"] = s.kid
	return token.SignedString(s.secret)
}
