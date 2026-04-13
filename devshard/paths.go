package devshard

import (
	"fmt"
	"strings"
)

const LegacyRoutePrefix = "/v1/devshard"

func VersionedRoutePrefix(version string) string {
	return "/devshard/" + version
}

func NormalizeRoutePrefix(routePrefix string) string {
	if routePrefix == "" {
		return LegacyRoutePrefix
	}
	return routePrefix
}

func SessionPayloadPath(routePrefix, escrowID string) string {
	normalized := strings.TrimPrefix(NormalizeRoutePrefix(routePrefix), "/")
	return fmt.Sprintf("%s/sessions/%s/payloads", normalized, escrowID)
}

func LegacySessionPayloadPath(escrowID string) string {
	return SessionPayloadPath(LegacyRoutePrefix, escrowID)
}

func VersionedSessionPayloadPath(version, escrowID string) string {
	return SessionPayloadPath(VersionedRoutePrefix(version), escrowID)
}
