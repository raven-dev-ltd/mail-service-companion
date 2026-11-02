package companion

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/client"
)

// Public routing decisions contain no credentials or employee information.
// Protocol operations remain on the mail server; only companion resources are
// dispatched to this service's handlers. Mailbox ACLs are checked at sign-in.
type featureRoute struct {
	Source               string `json:"source"`
	Protocol             string `json:"protocol"`
	Enabled              bool   `json:"enabled"`
	ServerExecution      bool   `json:"serverExecution,omitempty"`
	RequiresMailboxCheck bool   `json:"requiresMailboxCheck,omitempty"`
	Reason               string `json:"reason,omitempty"`
}
type routingConfig struct {
	SchemaVersion    int                     `json:"schemaVersion"`
	GeneratedAt      string                  `json:"generatedAt"`
	IMAPVerified     bool                    `json:"imapVerified"`
	IMAPCapabilities []string                `json:"imapCapabilities"`
	Features         map[string]featureRoute `json:"features"`
}

func (s *service) featureRouting() routingConfig {
	if s.routing != nil {
		return *s.routing
	}
	return s.defaultRouting()
}
func (s *service) defaultRouting() routingConfig {
	p := routingConfig{SchemaVersion: 1, GeneratedAt: time.Now().UTC().Format(time.RFC3339), IMAPCapabilities: []string{}, Features: map[string]featureRoute{}}
	for _, id := range []string{"mail.read", "mail.flags", "mail.move", "mail.search", "mail.drafts", "folderManagement"} {
		p.Features[id] = featureRoute{Source: "mail-server", Protocol: "IMAP", Enabled: true, RequiresMailboxCheck: true}
	}
	p.Features["mail.send"] = featureRoute{Source: "mail-server", Protocol: "SMTP", Enabled: true, RequiresMailboxCheck: true}
	p.Features["mail.idle"] = featureRoute{Source: "mail-server", Protocol: "IMAP IDLE", RequiresMailboxCheck: true}
	p.Features["rules"] = featureRoute{Source: "mail-server", Protocol: "ManageSieve", RequiresMailboxCheck: true, Reason: "Verify the configured ManageSieve endpoint and mailbox permissions at sign-in."}
	p.Features["calendar"] = featureRoute{Source: "mail-server", Protocol: "CalDAV", RequiresMailboxCheck: true, Reason: "Requires a supported calendar adapter and account discovery."}
	p.Features["notifications.poll"] = featureRoute{Source: "device", Protocol: "Android background checks", Enabled: true}
	for _, id := range []string{"identityProfile", "contacts", "contactLists", "templates", "labels", "scheduledSend", "campaigns", "push", "snooze"} {
		enabled := id != "snooze"
		reason := ""
		if id == "scheduledSend" || id == "campaigns" {
			enabled = s.deliveryConfigured()
			if !enabled {
				reason = "Administrator SMTP relay is not configured."
			}
		}
		if id == "push" {
			enabled = s.pushConfigured()
			if !enabled {
				reason = "Firebase credentials and authenticated mail-server events are not configured."
			}
		}
		if id == "snooze" {
			reason = "No server-side snooze executor is implemented."
		}
		protocol := "HTTPS companion API"
		if id == "labels" {
			protocol = "HTTPS label definitions + IMAP keywords"
		}
		p.Features[id] = featureRoute{Source: "companion", Protocol: protocol, Enabled: enabled, ServerExecution: enabled && (id == "scheduledSend" || id == "campaigns"), Reason: reason}
	}
	for _, id := range s.config.DisabledFeatures {
		route := p.Features[id]
		route.Enabled = false
		route.ServerExecution = false
		route.Reason = "Disabled by the organization's administrator."
		p.Features[id] = route
	}
	return p
}

// Verify TLS and inspect unauthenticated IMAP capabilities once at setup/startup.
// This must never infer mailbox write permissions or retain a login password.
func probeRoutingIMAP(ctx context.Context, c Config) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(c.IMAPHost, strconv.Itoa(c.IMAPPort)))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	var transport net.Conn = conn
	config := &tls.Config{ServerName: c.IMAPHost, MinVersion: tls.VersionTLS12}
	if c.IMAPSecurity == "implicit-tls" {
		secure := tls.Client(conn, config)
		if err = secure.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		transport = secure
	}
	imap, err := client.New(transport)
	if err != nil {
		return nil, err
	}
	if c.IMAPSecurity == "starttls" {
		if err = imap.StartTLS(config); err != nil {
			return nil, err
		}
	}
	caps, err := imap.Capability()
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(caps))
	for name := range caps {
		result = append(result, strings.ToUpper(name))
	}
	sort.Strings(result)
	return result, nil
}

func (s *service) configureRouting(ctx context.Context) error {
	p := s.defaultRouting()
	caps, err := probeRoutingIMAP(ctx, s.config)
	if err == nil {
		p.IMAPVerified = true
		p.IMAPCapabilities = caps
	}
	for _, id := range []string{"mail.read", "mail.flags", "mail.move", "mail.search", "mail.drafts", "folderManagement"} {
		route := p.Features[id]
		route.Enabled = p.IMAPVerified
		if !p.IMAPVerified {
			route.Reason = "Installation could not verify IMAP; the app will check again with the mailbox at sign-in."
		}
		p.Features[id] = route
	}
	for _, cap := range caps {
		if cap == "IDLE" {
			route := p.Features["mail.idle"]
			route.Enabled = true
			p.Features["mail.idle"] = route
		}
	}
	// The generated plan is replaced atomically after checking current configuration.
	// It is regenerated on restart so removed integrations never stay enabled.
	if err = os.MkdirAll(s.config.DataDir, 0700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(s.config.DataDir, ".feature-routing-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(body); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(s.config.DataDir, "feature-routing.json")); err != nil {
		return err
	}
	// Read and validate the exact configuration which will drive runtime routing.
	saved, err := os.ReadFile(filepath.Join(s.config.DataDir, "feature-routing.json"))
	if err != nil {
		return err
	}
	if err = json.Unmarshal(saved, &p); err != nil {
		return err
	}
	if p.SchemaVersion != 1 || len(p.Features) == 0 {
		return errors.New("invalid generated feature routing")
	}
	s.routing = &p
	return nil
}

func companionResource(path string) string {
	if path == "/api/profile" {
		return "identityProfile"
	}
	for _, entry := range [][2]string{{"/api/contact-lists", "contactLists"}, {"/api/contacts", "contacts"}, {"/api/avatars/image", "contacts"}, {"/api/templates", "templates"}, {"/api/labels", "labels"}, {"/api/campaigns", "campaigns"}, {"/api/mail/scheduled", "scheduledSend"}, {"/api/native/push", "push"}} {
		if path == entry[0] || strings.HasPrefix(path, entry[0]+"/") {
			return entry[1]
		}
	}
	return ""
}
func (s *service) routeFeatureRequest(w http.ResponseWriter, r *http.Request) bool {
	id := companionResource(r.URL.Path)
	// Ordinary mail sending stays direct SMTP; this endpoint only accepts jobs.
	if r.URL.Path == "/api/mail/send" {
		id = "scheduledSend"
	}
	if id == "" {
		return false
	}
	route := s.featureRouting().Features[id]
	// Existing handlers enforce their integration prerequisites as well as
	// ownership, verbs and payloads. Policy-disabled resources stop here.
	disabled := false
	for _, feature := range s.config.DisabledFeatures {
		if feature == id {
			disabled = true
		}
	}
	if route.Source != "companion" || disabled {
		reply(w, http.StatusNotImplemented, map[string]any{"error": "This feature is not enabled by the companion routing configuration.", "feature": id, "route": route})
		return true
	}
	if !route.Enabled {
		reply(w, http.StatusServiceUnavailable, map[string]any{"error": route.Reason, "feature": id, "route": route})
		return true
	}
	w.Header().Set("X-Mail-Feature-Source", route.Source)
	return false
}

func (s *service) dispatchFeature(w http.ResponseWriter, r *http.Request, owner string) bool {
	if s.routeFeatureRequest(w, r) {
		return true
	}
	id := companionResource(r.URL.Path)
	if r.URL.Path == "/api/mail/send" {
		id = "scheduledSend"
	}
	switch id {
	case "identityProfile":
		return s.identityProfile(w, r, owner)
	case "push":
		return s.pushRegistration(w, r, owner)
	case "scheduledSend":
		return s.delivery(w, r, owner)
	case "campaigns":
		return s.campaigns(w, r, owner)
	case "labels":
		return s.labels(w, r, owner)
	case "templates":
		return s.templates(w, r, owner)
	case "contacts", "contactLists":
		return s.contacts(w, r, owner)
	}
	return false
}
