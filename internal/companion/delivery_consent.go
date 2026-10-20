package companion

import "net/http"

// Keep the legacy URL for installed clients, but authorization is now supplied
// by the administrator's relay configuration, not a per-employee consent row.
func (s *service) deliveryConsent(w http.ResponseWriter, r *http.Request, owner string) bool {
	if r.URL.Path != "/api/delivery/consent" {
		return false
	}
	switch r.Method {
	case "GET":
		reply(w, 200, map[string]any{"configured": s.deliveryConfigured(), "enabled": s.deliveryConfigured(), "administratorManaged": true})
	case "PUT":
		var input struct {
			Consent bool `json:"consent"`
		}
		if !readJSON(w, r, &input) {
			return true
		}
		if !s.deliveryConfigured() {
			problem(w, 503, "The administrator has not configured an SMTP relay.")
		} else if !input.Consent {
			problem(w, 409, "Delivery is administrator-managed. Cancel individual scheduled messages or campaigns instead.")
		} else {
			reply(w, 200, map[string]any{"configured": true, "enabled": true, "administratorManaged": true})
		}
	case "DELETE":
		problem(w, 409, "Delivery is administrator-managed. Cancel individual scheduled messages or campaigns instead.")
	default:
		problem(w, 405, "Method not allowed.")
	}
	return true
}
