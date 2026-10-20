package companion

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPushEventsAndPasswordFreeRegistration(t *testing.T) {
	s := testService(t)
	token := login(t, s, "a@example.test")
	s.config.EventSecret = []byte(strings.Repeat("x", 32))
	s.push = &pushSender{project: "fixture"}
	body := `{"id":"registration-123456","token":"fixture-token-123456789","consent":true,"projectId":"fixture"}`
	request(t, s, "POST", "/api/native/push", token, body, 200)
	request(t, s, "POST", "/api/native/push", token, strings.TrimSuffix(body, "}")+`,"password":"forbidden"}`, 400)
	send := func(body string, offset int64, valid bool) int {
		stamp := strconv.FormatInt(time.Now().Unix()+offset, 10)
		mac := hmac.New(sha256.New, s.config.EventSecret)
		mac.Write([]byte(stamp + "." + body))
		r := httptest.NewRequest("POST", "/api/integrations/mail-events", strings.NewReader(body))
		r.Header.Set("X-Companion-Timestamp", stamp)
		if valid {
			r.Header.Set("X-Companion-Signature", hex.EncodeToString(mac.Sum(nil)))
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}
	event := `{"id":"event-1234567890123","mailbox":"a@example.test","validity":123,"lastUid":42}`
	if send(event, 0, false) != 401 || send(event, -301, true) != 401 || send(event, 0, true) != 202 || send(event, 0, true) != 202 {
		t.Fatal("event auth/replay handling")
	}
	var n int
	s.db.QueryRow("SELECT count(*) FROM push_jobs").Scan(&n)
	if n != 1 {
		t.Fatal("duplicate queued", n)
	}
	if send(strings.Replace(event, "42", "43", 1), 0, true) != 409 {
		t.Fatal("event collision accepted")
	}
	other := login(t, s, "b@example.test")
	request(t, s, "DELETE", "/api/native/push?id=registration-123456", other, "", 200)
	s.db.QueryRow("SELECT count(*) FROM push_jobs").Scan(&n)
	if n != 1 {
		t.Fatal("cross-account revoke")
	}
	request(t, s, "DELETE", "/api/native/push?id=registration-123456", token, "", 200)
	s.db.QueryRow("SELECT count(*) FROM push_jobs").Scan(&n)
	if n != 0 {
		t.Fatal("revocation did not cancel push")
	}
}
func TestFutureScheduledMailIsNotDeliveredImmediately(t *testing.T) {
	s, token := deliveryService(t)
	schedule(t, s, token)
	s.sendDelivery = func(context.Context, string, deliveryMessage, string) error { t.Error("sent future mail"); return nil }
	s.deliverOne(context.Background())
}
