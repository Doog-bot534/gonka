package middleware

import (
	"net/http"

	"decentralized-api/utils"

	"github.com/labstack/echo/v4"
)

// RequireApiVersion enforces that requests include the expected X-API-Version header.
// This is intended to gate new /v2 APIs during staged rollouts to avoid mixed-version peers.
func RequireApiVersion(expectedVersion string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			got := c.Request().Header.Get(utils.XApiVersionHeader)
			if got != expectedVersion {
				return echo.NewHTTPError(
					http.StatusPreconditionFailed,
					"X-API-Version mismatch",
				)
			}
			return next(c)
		}
	}
}
