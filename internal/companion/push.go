package companion

// Optional FCM wakeups: the server accepts authenticated delivery events, while the phone
// downloads private content directly from IMAP. No mail content goes to Google.
import (
	"bytes"
	"context"

	"encoding/json"
	"errors"
	"io"

	"net/http"
	"os"
	"strconv"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type pushSender struct {
	project string
	tokens  oauth2.TokenSource
	http    *http.Client
}

func newPushSender(ctx context.Context, path string) (*pushSender, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("cannot read server FCM credentials")
	}
	var identity struct {
		Type    string `json:"type"`
		Project string `json:"project_id"`
	}
	if json.Unmarshal(data, &identity) != nil || identity.Type != "service_account" || identity.Project == "" {
		return nil, errors.New("FCM requires a server service-account JSON")
	}
	credentials, err := google.JWTConfigFromJSON(data, "https://www.googleapis.com/auth/firebase.messaging")
	if err != nil {
		return nil, errors.New("invalid server FCM credentials")
	}
	// Do not allow credential JSON to redirect signing assertions elsewhere.
	credentials.TokenURL = "https://oauth2.googleapis.com/token"
	transport := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, transport)
	return &pushSender{identity.Project, credentials.TokenSource(ctx), transport}, nil
}
func (p *pushSender) send(ctx context.Context, token, id string, validity, last uint32) error {
	body, _ := json.Marshal(map[string]any{"message": map[string]any{"token": token, "android": map[string]any{"priority": "high", "ttl": "300s", "collapse_key": id}, "data": map[string]string{"type": "mail", "registrationId": id, "validity": strconv.FormatUint(uint64(validity), 10), "lastUid": strconv.FormatUint(uint64(last), 10), "count": "1"}}})
	access, err := p.tokens.Token()
	if err != nil {
		return errors.New("FCM authorization failed")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://fcm.googleapis.com/v1/projects/"+p.project+"/messages:send", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	response, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 16384))
	if response.StatusCode != 200 {
		return errors.New("FCM delivery rejected")
	}
	return nil
}
func (s *service) pushConfigured() bool { return s.push != nil && len(s.config.EventSecret) >= 32 }

func (s *service) pushRegistration(w http.ResponseWriter, r *http.Request, owner string) bool {
	if r.URL.Path != "/api/native/push" {
		return false
	}
	if r.Method == "DELETE" {
		_, err := s.db.ExecContext(r.Context(), "DELETE FROM push_registrations WHERE owner=? AND id=?", owner, r.URL.Query().Get("id"))
		if err != nil {
			problem(w, 503, "Could not revoke push.")
		} else {
			reply(w, 200, map[string]bool{"revoked": true})
		}
		return true
	}
	if r.Method != "POST" {
		problem(w, 405, "Method not allowed.")
		return true
	}
	if !s.pushConfigured() {
		problem(w, 503, "Companion Firebase push is not configured.")
		return true
	}
	var input struct {
		ID      string `json:"id"`
		Token   string `json:"token"`
		Consent bool   `json:"consent"`
		Project string `json:"projectId"`
	}
	if !readJSON(w, r, &input) {
		return true
	}
	if !input.Consent || len(input.ID) < 16 || len(input.ID) > 128 || len(input.Token) < 20 || len(input.Token) > 4096 || input.Project != s.push.project {
		problem(w, 400, "Consent and matching Firebase project are required.")
		return true
	}
	if !s.allowLogin("push:"+owner, 10) {
		problem(w, 429, "Too many registration attempts.")
		return true
	}
	_, _ = s.db.ExecContext(r.Context(), "DELETE FROM push_registrations WHERE expires<=?", time.Now().Unix())
	result, err := s.db.ExecContext(r.Context(), `INSERT INTO push_registrations(owner,id,token,expires) SELECT ?,?,?,? WHERE (SELECT count(*) FROM push_registrations WHERE owner=?)<10 AND (SELECT count(*) FROM push_registrations)<1000 OR EXISTS(SELECT 1 FROM push_registrations WHERE owner=? AND id=?) ON CONFLICT(owner,id) DO UPDATE SET token=excluded.token,expires=excluded.expires`, owner, input.ID, input.Token, time.Now().Add(7*24*time.Hour).Unix(), owner, owner, input.ID)
	if err != nil {
		problem(w, 503, "Could not register push.")
		return true
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		problem(w, 409, "Push device limit reached.")
		return true
	}
	reply(w, 200, map[string]any{"registered": true, "leaseDays": 7})
	return true
}
