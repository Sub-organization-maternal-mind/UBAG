package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

var fleetPaths = []string{"/v1/fleet/nodes", "/v1/fleet/summary"}

// fleet:read is held by operator and admin only (authz table pinned in
// internal/authz). With no fleet source wired, both of them see the documented
// 501, never a body, and the error carries no fleet detail.
func TestFleetRoutesReturn501ForFleetReaders(t *testing.T) {
	for _, role := range []string{"operator", "admin"} {
		server := NewServer(Config{AppSecret: "dev-secret", ActorRole: role}).Handler()
		for _, path := range fleetPaths {
			resp := doJSON(server, http.MethodGet, path, "", authHeaders(""))
			if resp.Code != http.StatusNotImplemented {
				t.Fatalf("%s GET %s = %d, want 501; body=%s", role, path, resp.Code, resp.Body.String())
			}
			var payload struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil || payload.Error.Code != "UBAG-NOT-IMPLEMENTED-001" {
				t.Fatalf("%s GET %s error = %+v (%v), want UBAG-NOT-IMPLEMENTED-001", role, path, payload, err)
			}
		}
	}
}

// Authorization runs before the 501: a role without fleet:read gets 403 on a
// deployment with no fleet, so the status never tells it whether one exists.
func TestFleetRoutesDenyRolesWithoutFleetRead(t *testing.T) {
	for _, role := range []string{"viewer", "developer", "service"} {
		server := NewServer(Config{AppSecret: "dev-secret", ActorRole: role}).Handler()
		for _, path := range fleetPaths {
			resp := doJSON(server, http.MethodGet, path, "", authHeaders(""))
			if resp.Code != http.StatusForbidden {
				t.Fatalf("%s GET %s = %d, want 403; body=%s", role, path, resp.Code, resp.Body.String())
			}
		}
	}
}

func TestFleetRoutesRequireAuthentication(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "operator"}).Handler()
	for _, path := range fleetPaths {
		resp := doJSON(server, http.MethodGet, path, "", map[string]string{"Ubag-Api-Version": DefaultAPIVersion})
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous GET %s = %d, want 401; body=%s", path, resp.Code, resp.Body.String())
		}
	}
}

func TestFleetRoutesAreReadOnly(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin"}).Handler()
	for _, path := range fleetPaths {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			resp := doJSON(server, method, path, "{}", authHeaders("idem-fleet-1"))
			if resp.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s = %d, want 405; body=%s", method, path, resp.Code, resp.Body.String())
			}
			if allow := resp.Header().Get("Allow"); allow != http.MethodGet {
				t.Fatalf("%s %s Allow = %q, want GET", method, path, allow)
			}
		}
	}
}
