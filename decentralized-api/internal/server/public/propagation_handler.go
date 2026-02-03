package public

import (
	"decentralized-api/poc/propagation"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

type PropagationHandlers struct {
	transport *propagation.HTTPTransport
	cache     *propagation.Cache
}

func NewPropagationHandlers(transport *propagation.HTTPTransport) *PropagationHandlers {
	return &PropagationHandlers{
		transport: transport,
	}
}

func (h *PropagationHandlers) SetCache(cache *propagation.Cache) {
	h.cache = cache
}

func (h *PropagationHandlers) HandleHeader(c echo.Context) error {
	h.transport.HandleHeaderHTTP(c.Response().Writer, c.Request())
	return nil
}

func (h *PropagationHandlers) HandleGetCache(c echo.Context) error {
	pocHeightStr := c.Param("poc_height")
	pocHeight, err := strconv.ParseInt(pocHeightStr, 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid poc_height")
	}

	if h.cache == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "cache not available")
	}

	bundles := h.cache.AllBundlesForHeight(pocHeight)

	response := map[string]interface{}{
		"poc_height": pocHeight,
		"count":      len(bundles),
	}

	return c.JSON(http.StatusOK, response)
}

func (h *PropagationHandlers) HandleProofs(c echo.Context) error {
	h.transport.HandleProofsHTTP(c.Response().Writer, c.Request())
	return nil
}

func (h *PropagationHandlers) HandleGetProofs(c echo.Context) error {
	bundleIDStr := c.Param("bundle_id")
	
	if len(bundleIDStr) != 64 {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid bundle_id: must be 64 hex characters")
	}

	var bundleID [32]byte
	for i := 0; i < 32; i++ {
		_, err := strconv.ParseUint(bundleIDStr[i*2:i*2+2], 16, 8)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid bundle_id: not valid hex")
		}
		val, _ := strconv.ParseUint(bundleIDStr[i*2:i*2+2], 16, 8)
		bundleID[i] = byte(val)
	}

	if h.cache == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "cache not available")
	}

	proofs, err := h.cache.GetProofs(bundleID)
	if err != nil {
		if err == propagation.ErrProofsNotFound {
			return echo.NewHTTPError(http.StatusNotFound, "proofs not found for bundle_id")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get proofs")
	}

	response := map[string]interface{}{
		"bundle_id": bundleIDStr,
		"proofs":    proofs,
	}

	return c.JSON(http.StatusOK, response)
}

func (h *PropagationHandlers) RegisterRoutes(e *echo.Group) {
	e.POST("propagation/header", h.HandleHeader)
	e.POST("propagation/proofs", h.HandleProofs)
	e.GET("propagation/cache/:poc_height", h.HandleGetCache)
	e.GET("propagation/proofs/:bundle_id", h.HandleGetProofs)
}
