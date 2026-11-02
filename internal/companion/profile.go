package companion

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

var profileFields = []string{"defaultFromName", "replyToAddress", "emailSignature", "signatureMode", "signatureTemplate", "signatureEnabled", "signaturePlacement"}

// Explicit allowlist: no password, directory/admin data, arbitrary JSON or avatar blobs.
func validProfile(input map[string]any) bool {
	if len(input) == 0 {
		return false
	}
	limits := map[string]int{"defaultFromName": 200, "replyToAddress": 254, "emailSignature": 65536}
	templateLimits := map[string]int{"name": 200, "jobTitle": 200, "phone": 80, "location": 300, "address": 500, "organizationName": 200, "websiteUrl": 2048, "logoUrl": 2048, "tagline": 300, "buttonText": 80, "accentColor": 7}
	validText := func(value any, limit int, multiline bool) bool {
		text, ok := value.(string)
		return ok && utf8.ValidString(text) && len(text) <= limit && !strings.ContainsRune(text, 0) && (multiline || !strings.ContainsAny(text, "\r\n"))
	}
	for key, value := range input {
		if limit, ok := limits[key]; ok {
			if !validText(value, limit, key == "emailSignature") {
				return false
			}
			if key == "replyToAddress" && value != "" && normalizedEmail(value.(string)) == "" {
				return false
			}
			continue
		}
		switch key {
		case "signatureEnabled":
			if _, ok := value.(bool); !ok {
				return false
			}
		case "signatureMode":
			if value != "custom" && value != "template" {
				return false
			}
		case "signaturePlacement":
			if value != "above" && value != "below" {
				return false
			}
		case "signatureTemplate":
			fields, ok := value.(map[string]any)
			if !ok {
				return false
			}
			for field, text := range fields {
				limit, ok := templateLimits[field]
				if !ok || !validText(text, limit, false) {
					return false
				}
				if (field == "websiteUrl" || field == "logoUrl") && text != "" {
					parsed, err := url.Parse(text.(string))
					if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
						return false
					}
				}
				if field == "accentColor" && text != "" && !validHexColor(text.(string)) {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}
func validHexColor(value string) bool {
	if len(value) != 7 || value[0] != '#' {
		return false
	}
	for _, r := range value[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

func (s *service) identityProfile(w http.ResponseWriter, r *http.Request, owner string) bool {
	if r.URL.Path != "/api/profile" {
		return false
	}
	respond := func(profile map[string]any) {
		reply(w, 200, map[string]any{"source": "companion", "profile": profile, "writableFields": profileFields, "success": true})
	}
	if r.Method == "GET" {
		var payload string
		err := s.db.QueryRowContext(r.Context(), "SELECT payload FROM identity_profiles WHERE owner=?", owner).Scan(&payload)
		if errors.Is(err, sql.ErrNoRows) {
			respond(map[string]any{})
			return true
		}
		var profile map[string]any
		if err != nil || json.Unmarshal([]byte(payload), &profile) != nil || !validProfile(profile) {
			problem(w, 503, "Your sender details could not be loaded.")
			return true
		}
		respond(profile)
		return true
	}
	if r.Method != "PUT" {
		problem(w, 405, "This action is not available.")
		return true
	}
	var input struct {
		Source  string         `json:"source"`
		Profile map[string]any `json:"profile"`
	}
	if !readJSON(w, r, &input) {
		return true
	}
	if input.Source != "companion" || !validProfile(input.Profile) {
		problem(w, 400, "Check your sender name, Reply-To address and signature details.")
		return true
	}
	payload, err := json.Marshal(input.Profile)
	if err == nil {
		_, err = s.db.ExecContext(r.Context(), "INSERT INTO identity_profiles(owner,payload,updated_at) VALUES(?,?,?) ON CONFLICT(owner) DO UPDATE SET payload=excluded.payload,updated_at=excluded.updated_at", owner, string(payload), time.Now().UTC().Format(time.RFC3339Nano))
	}
	if err != nil {
		problem(w, 503, "Your sender details could not be saved. Please try again.")
		return true
	}
	respond(input.Profile)
	return true
}
