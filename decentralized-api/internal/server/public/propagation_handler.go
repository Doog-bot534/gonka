package public

import (
	"decentralized-api/poc/propagation"

	"github.com/labstack/echo/v4"
)

type PropagationHandlers struct {
	transport *propagation.HTTPTransport
}

func NewPropagationHandlers(transport *propagation.HTTPTransport) *PropagationHandlers {
	return &PropagationHandlers{
		transport: transport,
	}
}

func (h *PropagationHandlers) HandleHeader(c echo.Context) error {
	h.transport.HandleHeaderHTTP(c.Response().Writer, c.Request())
	return nil
}

func (h *PropagationHandlers) RegisterRoutes(e *echo.Group) {
	e.POST("/propagation/header", h.HandleHeader)
}
