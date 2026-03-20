package versioned

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func setupTestServer(t *testing.T) (*echo.Echo, *Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "versions.json"), filepath.Join(dir, "bin"))
	if err != nil {
		t.Fatal(err)
	}

	e := echo.New()
	g := e.Group("/admin/v1/")
	RegisterRoutes(g, store)
	return e, store
}

func TestHandler_ListEmpty(t *testing.T) {
	e, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/versions", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var cfg VersionConfig
	json.NewDecoder(rec.Body).Decode(&cfg)
	if len(cfg.Versions) != 0 {
		t.Errorf("expected empty versions, got %d", len(cfg.Versions))
	}
}

func TestHandler_PutAndList(t *testing.T) {
	e, _ := setupTestServer(t)

	body := `{"binary":"http://example.com/v1.zip","sha256":"abc123","port":9001}`
	req := httptest.NewRequest(http.MethodPut, "/admin/v1/versions/v1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// List
	req = httptest.NewRequest(http.MethodGet, "/admin/v1/versions", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	var cfg VersionConfig
	json.NewDecoder(rec.Body).Decode(&cfg)
	if len(cfg.Versions) != 1 || cfg.Versions[0].Name != "v1" {
		t.Errorf("list after put: %+v", cfg)
	}
}

func TestHandler_PutNoChecksum(t *testing.T) {
	e, _ := setupTestServer(t)

	body := `{"binary":"http://example.com/v1.zip","port":9001}`
	req := httptest.NewRequest(http.MethodPut, "/admin/v1/versions/v1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandler_PutChecksumInURL(t *testing.T) {
	e, _ := setupTestServer(t)

	body := `{"binary":"http://example.com/v1.zip?checksum=sha256:abc123","port":9001}`
	req := httptest.NewRequest(http.MethodPut, "/admin/v1/versions/v1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestHandler_Delete(t *testing.T) {
	e, store := setupTestServer(t)
	store.Put(Version{Name: "v1", Binary: "http://a.com/v1.zip", SHA256: "abc", Port: 9001})

	req := httptest.NewRequest(http.MethodDelete, "/admin/v1/versions/v1", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}

	cfg := store.List()
	if len(cfg.Versions) != 0 {
		t.Errorf("expected empty after delete, got %+v", cfg)
	}
}

func TestHandler_DeleteNotFound(t *testing.T) {
	e, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodDelete, "/admin/v1/versions/nonexistent", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandler_ServeBinary(t *testing.T) {
	e, store := setupTestServer(t)

	// Create a test binary file
	binPath := filepath.Join(store.BinaryDir(), "test.bin")
	os.WriteFile(binPath, []byte("binary content"), 0644)

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/binaries/test.bin", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "binary content" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestHandler_ServeBinaryNotFound(t *testing.T) {
	e, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/binaries/nonexistent", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandler_ServeBinaryPathTraversal(t *testing.T) {
	e, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/binaries/..%2Fetc%2Fpasswd", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Should be rejected (either 400 or 404 is acceptable)
	if rec.Code == http.StatusOK {
		t.Fatal("path traversal should not return 200")
	}
}
