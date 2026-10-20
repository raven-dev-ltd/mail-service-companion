package companion

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSMTPVerifiesTLSAndCommitsBeforeQuit(t *testing.T) {
	for _, mode := range []string{"service", "ip-allowlist"} {
		t.Run(mode, func(t *testing.T) { testSMTPRelay(t, mode) })
	}
}

func testSMTPRelay(t *testing.T, mode string) {
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cert, leaf := fixture.TLS.Certificates[0], fixture.Certificate()
	fixture.Close()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	observed := make(chan string, 2)
	go func() {
		for i := 0; i < 2; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				if conn.(*tls.Conn).Handshake() != nil {
					observed <- "untrusted"
					return
				}
				fmt.Fprint(conn, "220 fixture SMTP\r\n")
				reader := bufio.NewReader(conn)
				data := false
				var wire strings.Builder
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if data {
						if line == ".\r\n" {
							fmt.Fprint(conn, "250 accepted\r\n")
							observed <- wire.String()
							return
						}
						wire.WriteString(line)
						continue
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						fmt.Fprint(conn, "250-fixture\r\n250 AUTH PLAIN\r\n")
					case strings.HasPrefix(line, "AUTH PLAIN "):
						if mode == "ip-allowlist" {
							t.Error("IP relay unexpectedly sent AUTH")
							return
						}
						if strings.TrimSpace(strings.TrimPrefix(line, "AUTH PLAIN ")) != base64.StdEncoding.EncodeToString([]byte("\x00relay-service\x00relay-test-secret")) {
							t.Error("not using administrator relay identity")
							return
						}
						fmt.Fprint(conn, "235 authenticated\r\n")
					case strings.HasPrefix(line, "MAIL FROM:"):
						fmt.Fprint(conn, "250 sender\r\n")
					case strings.HasPrefix(line, "RCPT TO:"):
						fmt.Fprint(conn, "250 recipient\r\n")
					case line == "DATA\r\n":
						fmt.Fprint(conn, "354 send body\r\n")
						data = true
					default:
						fmt.Fprint(conn, "500 unexpected command\r\n")
					}
				}
			}()
		}
	}()
	c := Config{SMTPMode: mode, SMTPUsername: "relay-service", SMTPPassword: "relay-test-secret", SMTPHost: "127.0.0.1", SMTPPort: listener.Addr().(*net.TCPAddr).Port, SMTPSecurity: "implicit-tls"}
	m := deliveryMessage{From: "a@example.test", To: []string{"b@example.test"}, BCC: []string{"private@example.test"}, Subject: "Hi", Text: "fixture only"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if smtpDelivery(c)(ctx, "a@example.test", m, strings.Repeat("a", 64)) == nil {
		t.Fatal("untrusted certificate accepted")
	}
	if <-observed != "untrusted" {
		t.Fatal("application data sent before TLS verification")
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	if err = smtpDeliveryWithRoots(c, roots)(ctx, "a@example.test", m, strings.Repeat("a", 64)); err != nil {
		t.Fatal("accepted DATA treated as failed QUIT", err)
	}
	if wire := <-observed; strings.Contains(wire, "private@example.test") || !strings.Contains(wire, "Subject: Hi") {
		t.Fatal("invalid message")
	}
}
