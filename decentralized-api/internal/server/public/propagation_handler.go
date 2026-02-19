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

func (h *PropagationHandlers) HandleGetFirstArrivals(c echo.Context) error {
	pocHeightStr := c.Param("poc_height")
	pocHeight, err := strconv.ParseInt(pocHeightStr, 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid poc_height")
	}

	if h.cache == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "cache not available")
	}

	arrivals, err := h.cache.GetAllFirstArrivals(pocHeight)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get first arrivals")
	}

	response := map[string]interface{}{
		"poc_height": pocHeight,
		"arrivals":   arrivals,
	}

	return c.JSON(http.StatusOK, response)
}

func (h *PropagationHandlers) RegisterRoutes(e *echo.Group) {
	e.POST("propagation/header", h.HandleHeader)
	e.GET("propagation/cache/:poc_height", h.HandleGetCache)
	e.GET("propagation/first-arrivals/:poc_height", h.HandleGetFirstArrivals)
}
