package companion

import (
	"testing"
)

func TestDeliveryConsentWithoutPasswords(t *testing.T) {
	s, token := deliveryService(t)
	request(t, s, "PUT", "/api/delivery/consent", token, `{"consent":true,"password":"must-not-be-accepted"}`, 400)
	other := login(t, s, "b@example.test")
	if request(t, s, "GET", "/api/delivery/consent", other, "", 200)["enabled"] != true {
		t.Fatal("administrator relay not enabled by default")
	}
	id := schedule(t, s, token)
	request(t, s, "DELETE", "/api/delivery/consent", other, "", 409)
	request(t, s, "DELETE", "/api/delivery/consent", token, "", 409)
	var status string
	s.db.QueryRow("SELECT status FROM delivery_jobs WHERE id=?", id).Scan(&status)
	if status != "PENDING" {
		t.Fatal("legacy consent endpoint changed pending sends")
	}
	var n int
	s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='delivery_credentials'").Scan(&n)
	if n != 0 {
		t.Fatal("legacy secret table remains")
	}
	s.db.QueryRow("SELECT count(*) FROM delivery_consents").Scan(&n)
	if n != 0 {
		t.Fatal("individual consent was stored")
	}
}
func TestRelayConfigurationValidation(t *testing.T) {
	t.Setenv("COMPANION_IMAP_HOST", "mail.example.test")
	t.Setenv("COMPANION_ALLOWED_DOMAINS", "example.test")
	t.Setenv("COMPANION_SMTP_MODE", "disabled")
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMPANION_SMTP_MODE", "service")
	if _, err := loadConfig(); err == nil {
		t.Fatal("service enabled without admin credentials")
	}
	t.Setenv("COMPANION_SMTP_MODE", "ip-allowlist")
	t.Setenv("COMPANION_SMTP_HOST", "relay.example.test")
	t.Setenv("COMPANION_SMTP_SECURITY", "none")
	if _, err := loadConfig(); err == nil {
		t.Fatal("plaintext relay accepted")
	}
	t.Setenv("COMPANION_SMTP_SECURITY", "starttls")
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
}
