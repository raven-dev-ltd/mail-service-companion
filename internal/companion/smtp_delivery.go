package companion

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

func encodedBody(value string) string {
	raw := base64.StdEncoding.EncodeToString([]byte(value))
	var out strings.Builder
	for len(raw) > 76 {
		out.WriteString(raw[:76] + "\r\n")
		raw = raw[76:]
	}
	out.WriteString(raw + "\r\n")
	return out.String()
}
func deliveryWire(owner string, m deliveryMessage, id string) []byte {
	var b bytes.Buffer
	from, _ := mail.ParseAddress(m.From)
	if m.FromName != "" {
		from.Name = m.FromName
	}
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\n", from.String(), strings.Join(m.To, ",\r\n "))
	if m.ReplyTo != "" {
		fmt.Fprintf(&b, "Reply-To: %s\r\n", (&mail.Address{Address: m.ReplyTo}).String())
	}
	if len(m.CC) > 0 {
		fmt.Fprintf(&b, "Cc: %s\r\n", strings.Join(m.CC, ",\r\n "))
	}
	fmt.Fprintf(&b, "Subject: %s\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nMIME-Version: 1.0\r\n", strings.ReplaceAll(mime.QEncoding.Encode("UTF-8", m.Subject), "?= =?", "?=\r\n =?"), time.Now().UTC().Format(time.RFC1123Z), id, strings.SplitN(owner, "@", 2)[1])
	if m.InReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", m.InReplyTo)
	}
	if len(m.References) > 0 {
		fmt.Fprintf(&b, "References: %s\r\n", strings.Join(m.References, "\r\n "))
	}
	boundary := "companion-" + id
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	for _, part := range []struct{ kind, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		if part.body == "" && (m.HTML != "" || m.Text != "") {
			continue
		}
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n%s", boundary, part.kind, encodedBody(part.body))
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes()
}
func smtpDelivery(c Config) func(context.Context, string, deliveryMessage, string) error {
	return smtpDeliveryWithRoots(c, nil)
}

// Root injection exists only for local TLS fixtures; deployment always uses
// the platform trust store and exposes no verification-bypass setting.
func smtpDeliveryWithRoots(c Config, roots *x509.CertPool) func(context.Context, string, deliveryMessage, string) error {
	return func(ctx context.Context, owner string, m deliveryMessage, id string) error {
		if (c.SMTPMode != "service" && c.SMTPMode != "ip-allowlist") || (c.SMTPSecurity != "starttls" && c.SMTPSecurity != "implicit-tls") {
			return errors.New("secure administrator relay is not configured")
		}
		if err := validateDelivery(owner, &m); err != nil {
			return err
		}
		conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(c.SMTPHost, strconv.Itoa(c.SMTPPort)))
		if err != nil {
			return err
		}
		defer conn.Close()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
		deadline := time.Now().Add(30 * time.Second)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		_ = conn.SetDeadline(deadline)
		tlsConfig := &tls.Config{ServerName: c.SMTPHost, MinVersion: tls.VersionTLS12, RootCAs: roots}
		var transport net.Conn = conn
		if c.SMTPSecurity == "implicit-tls" {
			secure := tls.Client(conn, tlsConfig)
			if err = secure.HandshakeContext(ctx); err != nil {
				return err
			}
			transport = secure
		}
		client, err := smtp.NewClient(transport, c.SMTPHost)
		if err != nil {
			return err
		}
		defer client.Close()
		if c.SMTPSecurity == "starttls" {
			if err = client.StartTLS(tlsConfig); err != nil {
				return err
			}
		}
		if c.SMTPMode == "service" {
			if err = client.Auth(smtp.PlainAuth("", c.SMTPUsername, c.SMTPPassword, c.SMTPHost)); err != nil {
				return err
			}
		}
		if err = client.Mail(owner); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, group := range [][]string{m.To, m.CC, m.BCC} {
			for _, recipient := range group {
				if seen[recipient] {
					continue
				}
				seen[recipient] = true
				if err = client.Rcpt(recipient); err != nil {
					return err
				}
			}
		}
		writer, err := client.Data()
		if err != nil {
			return err
		}
		if _, err = writer.Write(deliveryWire(owner, m, id)); err != nil {
			return err
		}
		// A successful DATA acknowledgement is the commit point. A subsequent QUIT
		// error must never turn an accepted message into a retried submission.
		if err = writer.Close(); err != nil {
			return err
		}
		_ = client.Quit()
		return nil
	}
}
