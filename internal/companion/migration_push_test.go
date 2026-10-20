package companion

import (
	"context"
	"encoding/json"
	"golang.org/x/oauth2"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type fcmFixture func(*http.Request) (*http.Response, error)

func (f fcmFixture) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFCMQueueContainsOnlyCursorAndRetriesAreBounded(t *testing.T) {
	s := testService(t)
	s.config.EventSecret = []byte(strings.Repeat("x", 32))
	calls := 0
	status := 503
	s.push = &pushSender{project: "fixture", tokens: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "fixture-access"}), http: &http.Client{Transport: fcmFixture(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "fcm.googleapis.com" || r.Header.Get("Authorization") != "Bearer fixture-access" {
			t.Error("wrong FCM destination/auth")
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil {
			t.Fatal("invalid FCM request")
		}
		data := body["message"].(map[string]any)["data"].(map[string]any)
		if len(data) != 5 || data["lastUid"] != "42" || data["validity"] != "123" {
			t.Error("wrong cursor")
		}
		if strings.Contains(string(raw), "a@example.test") || strings.Contains(string(raw), "password") || strings.Contains(string(raw), "subject") {
			t.Error("private mail leaked")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})}}
	now := time.Now().Unix()
	if _, err := s.db.Exec("INSERT INTO push_registrations VALUES(?,?,?,?)", "a@example.test", "device-1234567890", "fixture-token", now+3600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO push_jobs VALUES(?,?,?,?,?,?,?,?)", "event-1234567890", "a@example.test", "device-1234567890", 123, 42, 0, now+300, 0); err != nil {
		t.Fatal(err)
	}
	s.pushBatch(context.Background())
	var attempts int
	var due int64
	if err := s.db.QueryRow("SELECT attempts,due FROM push_jobs").Scan(&attempts, &due); err != nil || attempts != 1 || due <= now || calls != 1 {
		t.Fatal("failure did not back off", err, attempts, due, calls)
	}
	status = 200
	s.db.Exec("UPDATE push_jobs SET due=0")
	s.pushBatch(context.Background())
	var count int
	s.db.QueryRow("SELECT count(*) FROM push_jobs").Scan(&count)
	if count != 0 || calls != 2 {
		t.Fatal("successful job retained")
	}
	s.db.Exec("INSERT INTO push_jobs VALUES(?,?,?,?,?,?,?,?)", "expired-event-123", "a@example.test", "device-1234567890", 123, 43, 0, now-1, 0)
	s.pushBatch(context.Background())
	if calls != 2 {
		t.Fatal("expired event sent")
	}
}

func TestSchemaSixRemovesLegacyCredentialsAndPreservesContacts(t *testing.T) {
	directory := t.TempDir()
	db, err := openStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`DELETE FROM schema_version WHERE version>=6;
 INSERT INTO schema_version VALUES(5);
 DROP TABLE push_jobs; DROP TABLE push_registrations;
 CREATE TABLE push_registrations(owner TEXT,id TEXT,ciphertext BLOB);
 INSERT INTO push_registrations VALUES('a@example.test','legacy-device',X'010203');
 CREATE TABLE delivery_credentials(owner TEXT,ciphertext BLOB,consent_at TEXT);
 INSERT INTO delivery_credentials VALUES('a@example.test',X'010203','old');
 INSERT INTO contacts(owner,email,name) VALUES('a@example.test','b@example.test','Preserved');
 INSERT INTO delivery_jobs(owner,id,payload,due,status,created_at) VALUES('a@example.test','old-job','{}',0,'PENDING','old');
 `)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = openStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='delivery_credentials'").Scan(&n)
	if n != 0 {
		t.Fatal("legacy credentials survived")
	}
	db.QueryRow("SELECT count(*) FROM push_registrations").Scan(&n)
	if n != 0 {
		t.Fatal("legacy push survived")
	}
	db.QueryRow("SELECT count(*) FROM contacts WHERE name='Preserved'").Scan(&n)
	if n != 1 {
		t.Fatal("contact lost")
	}
	var status string
	db.QueryRow("SELECT status FROM delivery_jobs WHERE id='old-job'").Scan(&status)
	if status != "FAILED" {
		t.Fatal("legacy job was not cancelled")
	}
}
