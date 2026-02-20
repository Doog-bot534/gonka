package public

import (
	"io"
	"net/http"
	"strconv"

	"decentralized-api/logging"
	"decentralized-api/poc/propagation"
	propagationpb "decentralized-api/poc/propagation/proto"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/protobuf/proto"
)

type PropagationHandlers struct {
	receiver propagation.ReceiverHandler
	cache    *propagation.Cache
}

func NewPropagationHandlers(receiver propagation.ReceiverHandler) *PropagationHandlers {
	return &PropagationHandlers{
		receiver: receiver,
	}
}

func (h *PropagationHandlers) SetCache(cache *propagation.Cache) {
	h.cache = cache
}

func (h *PropagationHandlers) HandleHeader(c echo.Context) error {
	if c.Request().Method != http.MethodPost {
		return echo.NewHTTPError(http.StatusMethodNotAllowed, "Method not allowed")
	}

	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		logging.Warn("PropagationHandlers: failed to read header", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid request")
	}

	var pbHeader propagationpb.PropagationHeader
	if err := proto.Unmarshal(body, &pbHeader); err != nil {
		logging.Warn("PropagationHandlers: failed to decode header", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid request")
	}

	header, err := propagation.ProtoToHeader(&pbHeader)
	if err != nil {
		logging.Warn("PropagationHandlers: invalid header", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid request")
	}

	if h.receiver == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "No receiver registered")
	}

	if err := h.receiver.OnHeader(header, header.Participant); err != nil {
		logging.Warn("PropagationHandlers: header handler failed", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "Handler error")
	}

	return c.NoContent(http.StatusOK)
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
