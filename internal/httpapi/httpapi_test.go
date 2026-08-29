package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mehmtens/cloudlet/internal/auth"
	"github.com/mehmtens/cloudlet/internal/files"
)

type fakeStore struct{}

func (fakeStore) Put(context.Context, string, string, io.Reader, int64) error { return nil }
func (fakeStore) Delete(context.Context, string) error                        { return nil }
func (fakeStore) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "https://example.test/signed", nil
}

type fakeRepository struct{}

func (fakeRepository) Reserve(context.Context, uuid.UUID, int64) error { return nil }
func (fakeRepository) Release(context.Context, uuid.UUID, int64) error { return nil }
func (fakeRepository) Usage(context.Context, uuid.UUID) (files.StorageUsage, error) {
	return files.StorageUsage{QuotaBytes: 5 << 30}, nil
}

func (fakeRepository) Create(context.Context, uuid.UUID, files.File, string) error { return nil }
func (fakeRepository) List(context.Context, uuid.UUID, *uuid.UUID, int, int) ([]files.File, error) {
	return []files.File{}, nil
}
func (fakeRepository) Search(context.Context, uuid.UUID, string, int) ([]files.File, error) {
	return []files.File{}, nil
}
func (fakeRepository) ListAdvanced(context.Context, uuid.UUID, files.ListOptions) ([]files.File, error) {
	return []files.File{}, nil
}
func (fakeRepository) Get(context.Context, uuid.UUID, uuid.UUID) (files.StoredFile, error) {
	return files.StoredFile{}, nil
}
func (fakeRepository) Rename(_ context.Context, _, id uuid.UUID, name string) (files.File, error) {
	return files.File{ID: id, OriginalName: name}, nil
}
func (fakeRepository) Move(_ context.Context, _, id uuid.UUID, folderID *uuid.UUID) (files.File, error) {
	return files.File{ID: id, FolderID: folderID, OriginalName: "moved.txt"}, nil
}
func (fakeRepository) BulkMove(_ context.Context, _ uuid.UUID, ids []uuid.UUID, folderID *uuid.UUID) ([]files.File, error) {
	result := make([]files.File, len(ids))
	for index, id := range ids {
		result[index] = files.File{ID: id, FolderID: folderID}
	}
	return result, nil
}
func (fakeRepository) BulkSoftDelete(_ context.Context, _ uuid.UUID, _ []uuid.UUID) error {
	return nil
}
func (fakeRepository) SoftDelete(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (fakeRepository) ListTrash(context.Context, uuid.UUID, int, int) ([]files.TrashedFile, error) {
	return []files.TrashedFile{}, nil
}
func (fakeRepository) GetTrashed(context.Context, uuid.UUID, uuid.UUID) (files.StoredFile, error) {
	return files.StoredFile{ObjectKey: "object-key"}, nil
}
func (fakeRepository) Restore(_ context.Context, _, id uuid.UUID) (files.File, error) {
	return files.File{ID: id, OriginalName: "restored.txt"}, nil
}
func (fakeRepository) PermanentlyDelete(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (fakeRepository) TrashedVersionObjects(context.Context, uuid.UUID, uuid.UUID) ([]string, error) {
	return []string{"object-key"}, nil
}

type fakeUsers struct{}

func (fakeUsers) Create(context.Context, auth.User, string) error { return nil }
func (fakeUsers) CredentialsByEmail(context.Context, string) (auth.User, string, error) {
	return auth.User{}, "", nil
}

func testAuth() *auth.Service {
	return auth.NewService(fakeUsers{}, "01234567890123456789012345678901", time.Hour)
}
func testHandler() http.Handler {
	return New(files.NewService(fakeStore{}, fakeRepository{}), nil, nil, testAuth(), 1024)
}

func TestHealth(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("expected nosniff security header")
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("expected CSP security header")
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected request id")
	}
}

func TestReadinessFailureReturns503(t *testing.T) {
	handler := New(files.NewService(fakeStore{}, fakeRepository{}), nil, nil, testAuth(), 1024, Options{Ready: func(context.Context) error { return context.DeadlineExceeded }})
	request := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
}

func TestSecureModeAddsHSTS(t *testing.T) {
	handler := New(files.NewService(fakeStore{}, fakeRepository{}), nil, nil, testAuth(), 1024, Options{CookieSecure: true})
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("expected HSTS in secure mode")
	}
}

func TestInvalidFileID(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/files/not-a-uuid", nil)
	request.Header.Set("Authorization", "Bearer "+testToken(t))
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

func TestListRejectsExcessiveLimit(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/files?limit=101", nil)
	request.Header.Set("Authorization", "Bearer "+testToken(t))
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

func TestListRejectsInvalidSort(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/files?sort=unknown", nil)
	request.Header.Set("Authorization", "Bearer "+testToken(t))
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

func testToken(t *testing.T) string {
	t.Helper()
	_, token, err := testAuth().Register(context.Background(), "test@example.com", "password12345")
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestFilesRequireAuthentication(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/files", nil)
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

func TestCookieAuthenticationRequiresMatchingCSRFForMutation(t *testing.T) {
	id := uuid.New()
	request := httptest.NewRequest(http.MethodPatch, "/v1/files/"+id.String(), bytes.NewBufferString(`{"name":"safe.txt"}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: accessCookieName, Value: testToken(t)})
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected missing CSRF to return 403, got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPatch, "/v1/files/"+id.String(), bytes.NewBufferString(`{"name":"safe.txt"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", "matching-token")
	request.AddCookie(&http.Cookie{Name: accessCookieName, Value: testToken(t)})
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "matching-token"})
	response = httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected matching CSRF to pass, got %d", response.Code)
	}
}

func TestBearerAuthenticationIsExemptFromCSRF(t *testing.T) {
	id := uuid.New()
	request := httptest.NewRequest(http.MethodPatch, "/v1/files/"+id.String(), bytes.NewBufferString(`{"name":"safe.txt"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+testToken(t))
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected bearer request to pass without CSRF, got %d", response.Code)
	}
}

func TestCSRFIssuesReadableSameSiteCookie(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/auth/csrf", nil)
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != csrfCookieName || cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("expected readable SameSite=Lax CSRF cookie")
	}
}
