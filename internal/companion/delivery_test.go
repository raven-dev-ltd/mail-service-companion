package companion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func deliveryService(t *testing.T) (*service, string) {
	s := testService(t)
	s.config.SMTPMode = "ip-allowlist"
	s.config.SMTPHost = "mail.example.test"
	token := login(t, s, "a@example.test")
	return s, token
}
func schedule(t *testing.T, s *service, token string) string {
	m := deliveryMessage{From: "a@example.test", To: []string{"b@example.test"}, Subject: "Hello বাংলা", HTML: "<p>Hello</p>", ScheduledFor: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	raw, _ := json.Marshal(m)
	return request(t, s, "POST", "/api/mail/send", token, string(raw), 201)["jobId"].(string)
}
func TestScheduledQueueAndWorker(t *testing.T) {
	s, token := deliveryService(t)
	id := schedule(t, s, token)
	s.db.Exec("UPDATE delivery_jobs SET due=0")
	var calls int
	s.sendDelivery = func(ctx context.Context, owner string, m deliveryMessage, id string) error {
		calls++
		if owner != "a@example.test" || m.Subject != "Hello বাংলা" {
			t.Error("wrong delivery identity")
		}
		return nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.deliverOne(context.Background()) }()
	}
	wg.Wait()
	var status string
	s.db.QueryRow("SELECT status FROM delivery_jobs WHERE id=?", id).Scan(&status)
	if calls != 1 || status != "SENT" {
		t.Fatal(calls, status)
	}
	other := login(t, s, "b@example.test")
	if len(request(t, s, "GET", "/api/mail/scheduled", other, "", 200)["scheduled"].([]any)) != 0 {
		t.Fatal("cross-owner disclosure")
	}
	request(t, s, "DELETE", "/api/mail/scheduled", other, `{"id":"`+id+`"}`, 200)
}
func TestScheduledMessageWaitsAndPastDatesAreRejected(t *testing.T) {
	s, token := deliveryService(t)
	id := schedule(t, s, token)
	s.sendDelivery = func(context.Context, string, deliveryMessage, string) error {
		t.Fatal("future message sent immediately")
		return nil
	}
	s.deliverOne(context.Background())
	var status string
	s.db.QueryRow("SELECT status FROM delivery_jobs WHERE id=?", id).Scan(&status)
	if status != "PENDING" {
		t.Fatal(status)
	}
	m := deliveryMessage{To: []string{"b@example.test"}, Subject: "Past date", ScheduledFor: time.Now().Add(-10 * time.Second).UTC().Format(time.RFC3339)}
	raw, _ := json.Marshal(m)
	request(t, s, "POST", "/api/mail/send", token, string(raw), 400)
}
func TestCancellationIsIdempotentAndCannotEraseSentHistory(t *testing.T) {
	s, token := deliveryService(t)
	id := schedule(t, s, token)
	for i := 0; i < 2; i++ { request(t, s, "DELETE", "/api/mail/scheduled", token, `{"id":"`+id+`"}`, 200) }
	var status string
	s.db.QueryRow("SELECT status FROM delivery_jobs WHERE id=?", id).Scan(&status)
	if status != "CANCELLED" { t.Fatal(status) }
	s.db.Exec("UPDATE delivery_jobs SET status='SENT' WHERE id=?", id)
	request(t, s, "DELETE", "/api/mail/scheduled", token, `{"id":"`+id+`"}`, 409)
	s.db.QueryRow("SELECT status FROM delivery_jobs WHERE id=?", id).Scan(&status)
	if status != "SENT" { t.Fatal("sent history erased") }
}
func TestDeliveryFailuresNeverAutomaticallyResendAndCancellationWorks(t *testing.T) {
	s, token := deliveryService(t)
	id := schedule(t, s, token)
	s.db.Exec("UPDATE delivery_jobs SET due=0")
	calls := 0
	s.sendDelivery = func(context.Context, string, deliveryMessage, string) error {
		calls++
		return errors.New("SMTP reply lost")
	}
	s.deliverOne(context.Background())
	s.deliverOne(context.Background())
	if calls != 1 {
		t.Fatal("uncertain send retried")
	}
	var status string
	s.db.QueryRow("SELECT status FROM delivery_jobs WHERE id=?", id).Scan(&status)
	if status != "FAILED" {
		t.Fatal(status)
	}
	id = schedule(t, s, token)
	request(t, s, "DELETE", "/api/mail/scheduled", token, `{"id":"`+id+`"}`, 200)
	s.db.Exec("UPDATE delivery_jobs SET due=0")
	s.deliverOne(context.Background())
	if calls != 1 {
		t.Fatal("revoked job sent")
	}
	var pending int
	s.db.QueryRow("SELECT count(*) FROM delivery_jobs WHERE status='PENDING'").Scan(&pending)
	if pending != 0 {
		t.Fatal("pending work survived revocation")
	}
}
func TestDeliveryRejectsForgedSenderAttachmentsAndMissingRelay(t *testing.T) {
	s, token := deliveryService(t)
	for _, body := range []string{
		`{"from":"victim@example.test","to":["b@example.test"],"subject":"Hi","scheduledFor":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`,
		`{"attachments":[{"name":"lost.txt"}]}`,
		`{"to":["b@example.test"],"subject":"bad\\r\\nHeader","scheduledFor":"invalid"}`,
	} {
		request(t, s, "POST", "/api/mail/send", token, body, 400)
	}
	s.config.SMTPMode = "disabled"
	m := deliveryMessage{To: []string{"b@example.test"}, Subject: "Hello", ScheduledFor: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	raw, _ := json.Marshal(m)
	request(t, s, "POST", "/api/mail/send", token, string(raw), 503)
}
func TestCampaignSnapshotsPersonalizationAndDuplicateLaunch(t *testing.T) {
	s, token := deliveryService(t)
	listID := list(t, s, token, "Readers")
	s.db.Exec("INSERT INTO contacts(owner,email,name) VALUES('a@example.test','reader@example.test','<Reader> Example')")
	s.db.Exec("INSERT INTO memberships(owner,list_id,email) VALUES('a@example.test',?,'reader@example.test')", listID)
	template := request(t, s, "POST", "/api/templates", token, `{"name":"Welcome","subject":"Hi {{first_name}}","htmlBody":"<p>{{name}} {{email}}</p>"}`, 201)["template"].(map[string]any)["id"].(string)
	input, _ := json.Marshal(map[string]any{"name": "Launch", "templateId": template, "listId": listID})
	id := request(t, s, "POST", "/api/campaigns", token, string(input), 201)["campaign"].(map[string]any)["id"].(string)
	body := `{"action":"launch","campaignId":"` + id + `"}`
	request(t, s, "POST", "/api/campaigns", token, body, 200)
	request(t, s, "POST", "/api/campaigns", token, body, 409)
	var payload string
	s.db.QueryRow("SELECT payload FROM delivery_jobs WHERE campaign_id=?", id).Scan(&payload)
	var m deliveryMessage
	json.Unmarshal([]byte(payload), &m)
	if !strings.Contains(m.HTML, "&lt;Reader&gt;") || len(m.To) != 1 || m.To[0] != "reader@example.test" {
		t.Fatal("unsafe personalization", m)
	}
	other := login(t, s, "b@example.test")
	request(t, s, "GET", "/api/campaigns/"+id, other, "", 404)
	report := request(t, s, "GET", "/api/campaigns/"+id, token, "", 200)
	if len(report["recipients"].([]any)) != 1 {
		t.Fatal(report)
	}
	request(t, s, "DELETE", "/api/campaigns/"+id, token, "", 200)
	var count int
	s.db.QueryRow("SELECT count(*) FROM delivery_jobs WHERE campaign_id=?", id).Scan(&count)
	if count != 0 {
		t.Fatal("cancelled campaign still queued")
	}
}
func TestLabelsIsolationAndRename(t *testing.T) {
	s := testService(t)
	a := login(t, s, "a@example.test")
	b := login(t, s, "b@example.test")
	label := request(t, s, "POST", "/api/labels", a, `{"name":"Work","color":"#123456"}`, 200)["label"].(map[string]any)
	id := label["id"].(string)
	request(t, s, "POST", "/api/labels", a, `{"name":"Default color"}`, 200)
	request(t, s, "PUT", "/api/labels/"+id, b, `{"name":"Stolen","color":"#123456"}`, 404)
	request(t, s, "PUT", "/api/labels/"+id, a, `{"name":"Projects","color":"#654321"}`, 200)
	if len(request(t, s, "GET", "/api/labels", b, "", 200)["labels"].([]any)) != 0 {
		t.Fatal("labels leaked")
	}
	request(t, s, "POST", "/api/labels", a, `{"name":"Bad","color":"url(https://tracker.invalid)"}`, 400)
	request(t, s, "DELETE", "/api/labels/"+id, a, "", 200)
}

func TestRestartDoesNotReplayAnInFlightMessage(t *testing.T) {
	s, token := deliveryService(t)
	id := schedule(t, s, token)
	s.db.Exec("UPDATE delivery_jobs SET status='SENDING',due=0 WHERE id=?", id)
	s.sendDelivery = func(context.Context, string, deliveryMessage, string) error {
		t.Error("uncertain message was replayed")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	s.runDelivery(ctx)
	var status, note string
	if err := s.db.QueryRow("SELECT status,error FROM delivery_jobs WHERE id=?", id).Scan(&status, &note); err != nil || status != "FAILED" || !strings.Contains(note, "uncertain") {
		t.Fatal("restart failed to quarantine ambiguous submission", status, note, err)
	}
}
func TestMIMEKeepsBCCPrivateAndUnicodeEncoded(t *testing.T) {
	m := deliveryMessage{From: "a@example.test", To: []string{"b@example.test"}, BCC: []string{"secret@example.test"}, Subject: "বাংলা", HTML: "<p>বাংলা</p>"}
	wire := string(deliveryWire("a@example.test", m, strings.Repeat("a", 64)))
	if strings.Contains(wire, "secret@example.test") || !strings.Contains(wire, "=?UTF-8?") || !strings.Contains(wire, "Content-Transfer-Encoding: base64") {
		t.Fatal("unsafe MIME headers", wire)
	}
}
