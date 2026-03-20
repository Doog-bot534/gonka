package versioned

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
)

func RegisterRoutes(g *echo.Group, store *Store) {
	h := &handler{store: store}
	g.GET("versions", h.listVersions)
	g.PUT("versions/:name", h.putVersion)
	g.DELETE("versions/:name", h.deleteVersion)
	g.GET("binaries/:name", h.serveBinary)
}

type handler struct {
	store *Store
}

func (h *handler) listVersions(c echo.Context) error {
	return c.JSON(http.StatusOK, h.store.List())
}

func (h *handler) putVersion(c echo.Context) error {
	name := c.Param("name")
	var v Version
	if err := c.Bind(&v); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
	}
	v.Name = name

	// Validate checksum source exists
	if !hasChecksum(v) {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": "version must have sha256 field or ?checksum=sha256: in binary URL",
		})
	}

	if err := h.store.Put(v); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, v)
}

func (h *handler) deleteVersion(c echo.Context) error {
	name := c.Param("name")
	if err := h.store.Delete(name); err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *handler) serveBinary(c echo.Context) error {
	name := c.Param("name")
	// Prevent path traversal
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid name"})
	}
	path := filepath.Join(h.store.BinaryDir(), name)
	if _, err := os.Stat(path); err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "binary not found"})
	}
	return c.File(path)
}

func hasChecksum(v Version) bool {
	if v.SHA256 != "" {
		return true
	}
	u, err := url.Parse(v.Binary)
	if err != nil {
		return false
	}
	cs := u.Query().Get("checksum")
	return strings.HasPrefix(cs, "sha256:")
}
