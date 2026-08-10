package auth

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"knowflow/internal/apperror"
)

var (
	ErrEmailExists        = errors.New("email exists")
	ErrRefreshUnavailable = errors.New("refresh unavailable")
)

type StoredUser struct {
	User
	PasswordHash string
}

type Repository interface {
	Register(ctx context.Context, email, passwordHash, refreshHash string, refreshExpiresAt time.Time) (User, error)
	FindByEmail(ctx context.Context, email string) (StoredUser, error)
	FindByID(ctx context.Context, id string) (User, error)
	StoreRefreshToken(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error
	RotateRefreshToken(ctx context.Context, oldHash, newHash string, newExpiresAt, now time.Time) (User, error)
	RevokeRefreshToken(ctx context.Context, tokenHash string, now time.Time) error
}

type Service struct {
	repository Repository
	secret     string
	accessTTL  time.Duration
	refreshTTL time.Duration
	now        func() time.Time
}

func NewService(repository Repository, secret string, accessTTL, refreshTTL time.Duration) *Service {
	return &Service{repository: repository, secret: secret, accessTTL: accessTTL, refreshTTL: refreshTTL, now: time.Now}
}

func (s *Service) Register(ctx context.Context, credentials Credentials) (TokenPair, error) {
	email, err := normalizeEmail(credentials.Email)
	if err != nil {
		return TokenPair{}, err
	}
	if err := validatePassword(credentials.Password); err != nil {
		return TokenPair{}, err
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(credentials.Password), bcrypt.DefaultCost)
	if err != nil {
		return TokenPair{}, apperror.Wrap(http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", err)
	}
	refresh, refreshHash, err := newRefreshToken()
	if err != nil {
		return TokenPair{}, apperror.Wrap(http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", err)
	}
	now := s.now().UTC()
	user, err := s.repository.Register(ctx, email, string(passwordHash), refreshHash, now.Add(s.refreshTTL))
	if errors.Is(err, ErrEmailExists) {
		return TokenPair{}, apperror.New(http.StatusConflict, "EMAIL_ALREADY_REGISTERED", "email is already registered")
	}
	if err != nil {
		return TokenPair{}, apperror.Wrap(http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", err)
	}
	return s.makePair(user, refresh, now)
}

func (s *Service) Login(ctx context.Context, credentials Credentials) (TokenPair, error) {
	email, err := normalizeEmail(credentials.Email)
	if err != nil || credentials.Password == "" {
		return TokenPair{}, invalidCredentials()
	}
	stored, err := s.repository.FindByEmail(ctx, email)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte(credentials.Password)) != nil {
		return TokenPair{}, invalidCredentials()
	}
	if stored.Status != StatusActive {
		return TokenPair{}, apperror.New(http.StatusForbidden, "USER_DISABLED", "user account is disabled")
	}
	refresh, refreshHash, err := newRefreshToken()
	if err != nil {
		return TokenPair{}, apperror.Wrap(http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", err)
	}
	now := s.now().UTC()
	if err := s.repository.StoreRefreshToken(ctx, stored.ID, refreshHash, now.Add(s.refreshTTL)); err != nil {
		return TokenPair{}, apperror.Wrap(http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", err)
	}
	return s.makePair(stored.User, refresh, now)
}

func (s *Service) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return TokenPair{}, invalidRefresh()
	}
	newToken, newHash, err := newRefreshToken()
	if err != nil {
		return TokenPair{}, apperror.Wrap(http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", err)
	}
	now := s.now().UTC()
	user, err := s.repository.RotateRefreshToken(ctx, hashRefreshToken(refreshToken), newHash, now.Add(s.refreshTTL), now)
	if errors.Is(err, ErrRefreshUnavailable) {
		return TokenPair{}, invalidRefresh()
	}
	if err != nil {
		return TokenPair{}, apperror.Wrap(http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", err)
	}
	if user.Status != StatusActive {
		return TokenPair{}, apperror.New(http.StatusForbidden, "USER_DISABLED", "user account is disabled")
	}
	return s.makePair(user, newToken, now)
}

func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	if strings.TrimSpace(refreshToken) == "" {
		return nil
	}
	if err := s.repository.RevokeRefreshToken(ctx, hashRefreshToken(refreshToken), s.now().UTC()); err != nil {
		return apperror.Wrap(http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", err)
	}
	return nil
}

func (s *Service) Authenticate(ctx context.Context, accessToken string) (User, error) {
	principal, err := verifyAccessToken(s.secret, accessToken, s.now().UTC())
	if err != nil {
		return User{}, apperror.New(http.StatusUnauthorized, "INVALID_ACCESS_TOKEN", "access token is invalid or expired")
	}
	user, err := s.repository.FindByID(ctx, principal.UserID)
	if err != nil {
		return User{}, apperror.New(http.StatusUnauthorized, "INVALID_ACCESS_TOKEN", "access token is invalid or expired")
	}
	if user.Status != StatusActive {
		return User{}, apperror.New(http.StatusForbidden, "USER_DISABLED", "user account is disabled")
	}
	return user, nil
}

func (s *Service) makePair(user User, refresh string, now time.Time) (TokenPair, error) {
	access, err := signAccessToken(s.secret, user, now, s.accessTTL)
	if err != nil {
		return TokenPair{}, apperror.Wrap(http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", err)
	}
	return TokenPair{
		AccessToken: access, RefreshToken: refresh, TokenType: "Bearer",
		AccessTokenExpiresIn: int64(s.accessTTL.Seconds()), RefreshTokenExpiresIn: int64(s.refreshTTL.Seconds()), User: user,
	}, nil
}

func normalizeEmail(value string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(value))
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || len(email) > 320 || !strings.Contains(email, "@") {
		return "", apperror.New(http.StatusBadRequest, "INVALID_EMAIL", "email is invalid")
	}
	return email, nil
}

func validatePassword(password string) error {
	length := utf8.RuneCountInString(password)
	if length < 8 || len(password) > 72 {
		return apperror.New(http.StatusBadRequest, "INVALID_PASSWORD", "password must be between 8 characters and 72 bytes")
	}
	return nil
}

func invalidCredentials() error {
	return apperror.New(http.StatusUnauthorized, "INVALID_CREDENTIALS", "email or password is incorrect")
}

func invalidRefresh() error {
	return apperror.New(http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "refresh token is invalid, expired, or revoked")
}
