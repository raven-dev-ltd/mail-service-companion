package companion

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedRoutingSurvivesUnavailableMailServerAndGatesResources(t *testing.T) {
	s := testService(t)
	s.config.DataDir = t.TempDir()
	s.config.IMAPHost = "127.0.0.1"
	s.config.IMAPPort = 0
	s.config.IMAPSecurity = "implicit-tls"
	s.config.DisabledFeatures = []string{"templates"}
	if err := s.configureRouting(context.Background()); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(filepath.Join(s.config.DataDir, "feature-routing.json"))
	if err != nil {
		t.Fatal(err)
	}
	var plan routingConfig
	if err = json.Unmarshal(bytes, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.SchemaVersion != 1 || plan.IMAPVerified || plan.Features["mail.read"].Enabled || plan.Features["templates"].Enabled || !plan.Features["contacts"].Enabled {
		t.Fatalf("bad routing: %+v", plan)
	}
	if plan.Features["mail.read"].Source != "mail-server" || !plan.Features["mail.read"].RequiresMailboxCheck {
		t.Fatal("mail ownership or account check lost")
	}
	token := login(t, s, "a@example.test")
	response := request(t, s, "GET", "/api/native/capabilities", token, "", 200)
	if response["routing"].(map[string]any)["imapVerified"] != false {
		t.Fatal("invented verified protocol")
	}
	request(t, s, "GET", "/api/templates", token, "", 501)
	request(t, s, "GET", "/api/contacts", token, "", 200)
	// Reconfiguration regenerates the file; removing an override restores a resource.
	s.config.DisabledFeatures = nil
	if err = s.configureRouting(context.Background()); err != nil {
		t.Fatal(err)
	}
	request(t, s, "GET", "/api/templates", token, "", 200)
}

func TestRoutingDistinguishesStoredDataFromExecution(t *testing.T) {
	s := testService(t)
	p := s.defaultRouting()
	if !p.Features["templates"].Enabled || p.Features["scheduledSend"].Enabled || p.Features["scheduledSend"].ServerExecution || p.Features["push"].Enabled {
		t.Fatal("missing prerequisites were ignored")
	}
	s.config.SMTPMode = "ip-allowlist"
	s.config.SMTPHost = "smtp.example.test"
	p = s.defaultRouting()
	if !p.Features["scheduledSend"].Enabled || !p.Features["scheduledSend"].ServerExecution || !p.Features["campaigns"].Enabled {
		t.Fatal("configured execution unavailable")
	}
}
