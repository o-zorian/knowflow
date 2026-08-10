package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type memoryRepository struct {
	user         StoredUser
	refresh      map[string]bool
	storedHashes []string
}

func (r *memoryRepository) Register(_ context.Context, email, passwordHash, refreshHash string, _ time.Time) (User, error) {
	if r.user.ID != "" {
		return User{}, ErrEmailExists
	}
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	r.user = StoredUser{User: User{ID: "user-1", Email: email, Role: RoleUser, Status: StatusActive, CreatedAt: now, UpdatedAt: now}, PasswordHash: passwordHash}
	if r.refresh == nil {
		r.refresh = map[string]bool{}
	}
	r.refresh[refreshHash] = true
	r.storedHashes = append(r.storedHashes, refreshHash)
	return r.user.User, nil
}

func (r *memoryRepository) FindByEmail(_ context.Context, email string) (StoredUser, error) {
	if r.user.Email != email {
		return StoredUser{}, errors.New("not found")
	}
	return r.user, nil
}

func (r *memoryRepository) FindByID(_ context.Context, id string) (User, error) {
	if r.user.ID != id {
		return User{}, errors.New("not found")
	}
	return r.user.User, nil
}

func (r *memoryRepository) StoreRefreshToken(_ context.Context, _ string, tokenHash string, _ time.Time) error {
	if r.refresh == nil {
		r.refresh = map[string]bool{}
	}
	r.refresh[tokenHash] = true
	r.storedHashes = append(r.storedHashes, tokenHash)
	return nil
}

func (r *memoryRepository) RotateRefreshToken(_ context.Context, oldHash, newHash string, _ time.Time, _ time.Time) (User, error) {
	if !r.refresh[oldHash] {
		return User{}, ErrRefreshUnavailable
	}
	delete(r.refresh, oldHash)
	r.refresh[newHash] = true
	r.storedHashes = append(r.storedHashes, newHash)
	return r.user.User, nil
}

func (r *memoryRepository) RevokeRefreshToken(_ context.Context, tokenHash string, _ time.Time) error {
	delete(r.refresh, tokenHash)
	return nil
}

func TestAuthenticationLifecycleHashesAndRevokesRefreshTokens(t *testing.T) {
	repository := &memoryRepository{}
	service := NewService(repository, "test-secret-with-enough-entropy", 2*time.Hour, 30*24*time.Hour)
	fixed := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixed }

	registered, err := service.Register(context.Background(), Credentials{Email: " User@Example.COM ", Password: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	if registered.User.Email != "user@example.com" || registered.User.Role != RoleUser || registered.User.Status != StatusActive {
		t.Fatalf("unexpected registered user: %+v", registered.User)
	}
	if repository.user.PasswordHash == "correct horse battery staple" || bcrypt.CompareHashAndPassword([]byte(repository.user.PasswordHash), []byte("correct horse battery staple")) != nil {
		t.Fatal("password was not stored as a bcrypt hash")
	}
	if repository.storedHashes[0] == registered.RefreshToken || repository.storedHashes[0] != hashRefreshToken(registered.RefreshToken) {
		t.Fatal("refresh token was not stored exclusively as its hash")
	}
	if _, err := service.Authenticate(context.Background(), registered.AccessToken); err != nil {
		t.Fatalf("authenticate access token: %v", err)
	}

	loggedIn, err := service.Login(context.Background(), Credentials{Email: "user@example.com", Password: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := service.Refresh(context.Background(), loggedIn.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.RefreshToken == loggedIn.RefreshToken {
		t.Fatal("refresh did not rotate the token")
	}
	if _, err := service.Refresh(context.Background(), loggedIn.RefreshToken); err == nil {
		t.Fatal("rotated refresh token remained usable")
	}
	if err := service.Logout(context.Background(), rotated.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Refresh(context.Background(), rotated.RefreshToken); err == nil {
		t.Fatal("logged out refresh token remained usable")
	}
}

func TestAccessTokenRejectsTamperingAndExpiry(t *testing.T) {
	user := User{ID: "user-1", Role: RoleUser}
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	token, err := signAccessToken("secret", user, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyAccessToken("different", token, now); err == nil {
		t.Fatal("token signed by another secret was accepted")
	}
	if _, err := verifyAccessToken("secret", token, now.Add(2*time.Hour)); err == nil {
		t.Fatal("expired token was accepted")
	}
}
