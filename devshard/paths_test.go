package devshard

import "testing"

func TestNormalizeRoutePrefixDefaultsToLegacy(t *testing.T) {
	if got := NormalizeRoutePrefix(""); got != LegacyRoutePrefix {
		t.Fatalf("NormalizeRoutePrefix(\"\") = %q, want %q", got, LegacyRoutePrefix)
	}
}

func TestResolveVersionedRoutePrefix(t *testing.T) {
	if got := ResolveVersionedRoutePrefix("v1", ""); got != VersionedRoutePrefix("v1") {
		t.Fatalf("ResolveVersionedRoutePrefix(\"v1\", \"\") = %q, want %q", got, VersionedRoutePrefix("v1"))
	}
	if got := ResolveVersionedRoutePrefix("v1", LegacyRoutePrefix); got != LegacyRoutePrefix {
		t.Fatalf("ResolveVersionedRoutePrefix override = %q, want %q", got, LegacyRoutePrefix)
	}
}

func TestSessionPayloadPath(t *testing.T) {
	tests := []struct {
		name        string
		routePrefix string
		escrowID    string
		want        string
	}{
		{
			name:        "legacy",
			routePrefix: "",
			escrowID:    "1",
			want:        "v1/devshard/sessions/1/payloads",
		},
		{
			name:        "versioned",
			routePrefix: VersionedRoutePrefix("v1"),
			escrowID:    "1",
			want:        "devshard/v1/sessions/1/payloads",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SessionPayloadPath(tt.routePrefix, tt.escrowID); got != tt.want {
				t.Fatalf("SessionPayloadPath(%q, %q) = %q, want %q", tt.routePrefix, tt.escrowID, got, tt.want)
			}
		})
	}
}
