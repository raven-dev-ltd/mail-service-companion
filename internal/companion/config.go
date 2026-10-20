package companion

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/client"
)

type Config struct {
	Listen, DataDir, IMAPHost, IMAPSecurity string
	IMAPPort                                int
	Domains, Origins                        []string
	SMTPMode, SMTPUsername, SMTPPassword    string
	EventSecret                             []byte
	SMTPHost, SMTPSecurity                  string
	SMTPPort                                int
	SendInterval                            time.Duration
	DisabledFeatures                        []string
}

func loadConfig() (Config, error) {
	c := Config{Listen: env("COMPANION_LISTEN", "127.0.0.1:8080"), DataDir: env("COMPANION_DATA_DIR", "./data"), IMAPHost: os.Getenv("COMPANION_IMAP_HOST"), IMAPSecurity: env("COMPANION_IMAP_SECURITY", "implicit-tls")}
	for _, id := range strings.Split(os.Getenv("COMPANION_DISABLED_FEATURES"), ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		switch id {
		case "contacts", "contactLists", "templates", "labels", "scheduledSend", "campaigns", "push":
			c.DisabledFeatures = append(c.DisabledFeatures, id)
		default:
			return c, errors.New("unknown COMPANION_DISABLED_FEATURES entry")
		}
	}
	c.SMTPMode = env("COMPANION_SMTP_MODE", "disabled")
	if c.SMTPMode != "disabled" && c.SMTPMode != "service" && c.SMTPMode != "ip-allowlist" {
		return c, errors.New("invalid SMTP mode")
	}
	c.SMTPUsername = os.Getenv("COMPANION_SMTP_USERNAME")
	if c.SMTPMode == "service" {
		secret, err := os.ReadFile(os.Getenv("COMPANION_SMTP_PASSWORD_FILE"))
		if err != nil || len(secret) == 0 || len(secret) > 4096 || c.SMTPUsername == "" {
			return c, errors.New("service relay requires administrator username and password file")
		}
		c.SMTPPassword = strings.TrimRight(string(secret), "\r\n")
		if c.SMTPPassword == "" {
			return c, errors.New("empty SMTP password file")
		}
	}
	if path := os.Getenv("COMPANION_EVENT_SECRET_FILE"); path != "" {
		secret, err := os.ReadFile(path)
		if err != nil {
			return c, errors.New("cannot read event secret file")
		}
		c.EventSecret = []byte(strings.TrimSpace(string(secret)))
		if len(c.EventSecret) < 32 || len(c.EventSecret) > 4096 {
			return c, errors.New("event secret must contain 32 to 4096 bytes")
		}
	}
	if (len(c.EventSecret) > 0) != (os.Getenv("COMPANION_FCM_CREDENTIALS") != "") {
		return c, errors.New("event push requires both COMPANION_EVENT_SECRET_FILE and COMPANION_FCM_CREDENTIALS")
	}
	port := "993"
	if c.IMAPSecurity == "starttls" {
		port = "143"
	}
	var err error
	c.SMTPHost = os.Getenv("COMPANION_SMTP_HOST")
	c.SMTPSecurity = env("COMPANION_SMTP_SECURITY", "starttls")
	c.SMTPPort, err = strconv.Atoi(env("COMPANION_SMTP_PORT", "587"))
	if err != nil || c.SMTPPort < 1 || c.SMTPPort > 65535 {
		return c, errors.New("invalid SMTP port")
	}
	c.SendInterval = 10 * time.Second
	if c.SMTPMode != "disabled" && (c.SMTPHost == "" || strings.ContainsAny(c.SMTPHost, "/@?#\r\n ") || (c.SMTPSecurity != "starttls" && c.SMTPSecurity != "implicit-tls")) {
		return c, errors.New("delivery requires a fixed SMTP hostname and verified TLS")
	}
	c.IMAPPort, err = strconv.Atoi(env("COMPANION_IMAP_PORT", port))
	if err != nil || c.IMAPPort < 1 || c.IMAPPort > 65535 || c.IMAPHost == "" || strings.ContainsAny(c.IMAPHost, "/@?#\r\n ") {
		return c, errors.New("set a valid COMPANION_IMAP_HOST and port")
	}
	if c.IMAPSecurity != "implicit-tls" && c.IMAPSecurity != "starttls" {
		return c, errors.New("IMAP security must be implicit-tls or starttls; plaintext is not supported")
	}
	for _, domain := range strings.Split(os.Getenv("COMPANION_ALLOWED_DOMAINS"), ",") {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain != "" {
			if !strings.Contains(domain, ".") || strings.ContainsAny(domain, "/:@?#* \r\n") {
				return c, errors.New("invalid allowed mailbox domain")
			}
			c.Domains = append(c.Domains, domain)
		}
	}
	if len(c.Domains) == 0 {
		return c, errors.New("set COMPANION_ALLOWED_DOMAINS to your mailbox domain(s)")
	}
	c.Origins = strings.Split(env("COMPANION_ALLOWED_ORIGINS", "https://localhost,capacitor://localhost"), ",")
	for i := range c.Origins {
		c.Origins[i] = strings.TrimSpace(c.Origins[i])
		if c.Origins[i] != "capacitor://localhost" && (!strings.HasPrefix(c.Origins[i], "https://") || strings.ContainsAny(strings.TrimPrefix(c.Origins[i], "https://"), "/?#@* \r\n")) {
			return c, errors.New("allowed origins must be exact HTTPS origins or capacitor://localhost")
		}
	}
	return c, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// Validate against the operator's fixed endpoint, never a hostname supplied by
// a phone. No credentials are retained and no mail is read or changed here.
func mailboxAuthenticator(c Config) func(context.Context, string, string) error {
	return mailboxAuthenticatorWithRoots(c, nil)
}

// Root injection is internal for TLS integration tests. The operator-facing
// service always uses the system trust store and cannot disable verification.
func mailboxAuthenticatorWithRoots(c Config, roots *x509.CertPool) func(context.Context, string, string) error {
	return func(ctx context.Context, email, password string) error {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		address := net.JoinHostPort(c.IMAPHost, strconv.Itoa(c.IMAPPort))
		conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
		if err != nil {
			return err
		}
		raw := conn
		defer raw.Close()
		stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
		defer stop()
		deadline, _ := ctx.Deadline()
		if err = conn.SetDeadline(deadline); err != nil {
			return err
		}
		tlsConfig := &tls.Config{ServerName: c.IMAPHost, MinVersion: tls.VersionTLS12, RootCAs: roots}
		if c.IMAPSecurity == "implicit-tls" {
			secure := tls.Client(conn, tlsConfig)
			if err = secure.HandshakeContext(ctx); err != nil {
				return err
			}
			conn = secure
		}
		imap, err := client.New(conn)
		if err != nil {
			return err
		}
		if c.IMAPSecurity == "starttls" {
			if err = imap.StartTLS(tlsConfig); err != nil {
				return err
			}
		}
		return imap.Login(email, password)
	}
}
