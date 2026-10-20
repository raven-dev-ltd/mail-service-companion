package companion

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMailboxAuthenticationUsesVerifiedTLSAndRealLogin(t *testing.T) {
	certServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cert := certServer.TLS.Certificates[0]
	leaf := certServer.Certificate()
	certServer.Close()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	logins := make(chan bool, 2)
	go func() {
		for i := 0; i < 2; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				if _, err = fmt.Fprint(conn, "* OK [CAPABILITY IMAP4rev1] Ready\r\n"); err != nil {
					logins <- false
					return
				}
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						logins <- false
						return
					}
					fields := strings.Fields(line)
					if len(fields) < 2 {
						logins <- false
						return
					}
					if fields[1] == "CAPABILITY" {
						_, _ = fmt.Fprintf(conn, "* CAPABILITY IMAP4rev1\r\n%s OK done\r\n", fields[0])
						continue
					}
					if fields[1] == "LOGIN" {
						valid := strings.Contains(line, "a@example.test") && strings.Contains(line, "fixture-password")
						logins <- valid
						status := "NO"
						if valid {
							status = "OK"
						}
						_, _ = fmt.Fprintf(conn, "%s %s login\r\n", fields[0], status)
						return
					}
				}
			}()
		}
	}()
	c := Config{IMAPHost: "127.0.0.1", IMAPPort: listener.Addr().(*net.TCPAddr).Port, IMAPSecurity: "implicit-tls"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = mailboxAuthenticator(c)(ctx, "a@example.test", "fixture-password"); err == nil {
		t.Fatal("untrusted certificate was accepted")
	}
	if <-logins {
		t.Fatal("credentials were sent before certificate verification")
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	if err = mailboxAuthenticatorWithRoots(c, roots)(ctx, "a@example.test", "fixture-password"); err != nil {
		t.Fatal(err)
	}
	if !<-logins {
		t.Fatal("verified connection did not perform mailbox LOGIN")
	}
}

func testService(t *testing.T) *service {
	t.Helper()
	db, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return newService(db, Config{Domains: []string{"example.test"}, Origins: []string{"https://localhost"}}, func(_ context.Context, _, password string) error {
		if password != "test-only-password" {
			return errors.New("rejected")
		}
		return nil
	})
}
func request(t *testing.T, s *service, method, path, token, body string, status int) map[string]any {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func login(t *testing.T, s *service, email string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"email": email, "password": "test-only-password", "rememberMe": true})
	return request(t, s, "POST", "/api/native/auth/login", "", string(body), 200)["accessToken"].(string)
}
func list(t *testing.T, s *service, token, name string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name})
	return request(t, s, "POST", "/api/contact-lists", token, string(body), 201)["list"].(map[string]any)["id"].(string)
}

func TestAuthenticationAndRevocation(t *testing.T) {
	s := testService(t)
	request(t, s, "GET", "/api/contacts", "", "", 401)
	request(t, s, "POST", "/api/native/auth/login", "", `{"email":"other@external.test","password":"test-only-password"}`, 401)
	request(t, s, "POST", "/api/native/auth/login", "", `{"email":"one@example.test","password":"wrong"}`, 401)
	token := login(t, s, "ONE@example.test")
	var hash string
	if err := s.db.QueryRow("SELECT token_hash FROM sessions").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash == token || hash != tokenHash(token) {
		t.Fatal("raw bearer token was persisted")
	}
	response := request(t, s, "GET", "/api/native/auth/session", token, "", 200)
	if response["user"].(map[string]any)["email"] != "one@example.test" || response["accessToken"] != nil {
		t.Fatal(response)
	}
	manifest := request(t, s, "GET", "/api/native/capabilities", token, "", 200)
	features := manifest["features"].(map[string]any)
	if features["contacts"] != true || features["push"] != false || features["scheduledSend"].(map[string]any)["serverExecution"] != false {
		t.Fatal(manifest)
	}
	request(t, s, "POST", "/api/native/auth/logout", token, "", 200)
	request(t, s, "GET", "/api/contacts", token, "", 401)
	token = login(t, s, "one@example.test")
	if _, err := s.db.Exec("UPDATE sessions SET expires=?", time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	request(t, s, "GET", "/api/contacts", token, "", 401)
}

func TestContactsAndListsAreIsolatedAndPreserved(t *testing.T) {
	s := testService(t)
	alice := login(t, s, "alice@example.test")
	bob := login(t, s, "bob@example.test")
	request(t, s, "POST", "/api/contacts/profile", alice, `{"email":"friend@elsewhere.test","name":"বন্ধু"}`, 201)
	a := list(t, s, alice, "Team")
	b := list(t, s, bob, "Private")
	request(t, s, "PUT", "/api/contacts/lists", alice, `{"email":"friend@elsewhere.test","listIds":["`+b+`"]}`, 404)
	request(t, s, "PUT", "/api/contacts/lists", alice, `{"email":"friend@elsewhere.test","listIds":["`+a+`"]}`, 200)
	request(t, s, "PATCH", "/api/contact-lists/"+a, bob, `{"name":"Hijacked"}`, 404)
	request(t, s, "DELETE", "/api/contact-lists/"+a, bob, "", 404)
	request(t, s, "GET", "/api/contact-lists/"+a+"/contacts", bob, "", 404)
	book := request(t, s, "GET", "/api/contacts", alice, "", 200)["contacts"].([]any)
	if len(book) != 1 || book[0].(map[string]any)["name"] != "বন্ধু" || len(book[0].(map[string]any)["lists"].([]any)) != 1 {
		t.Fatal(book)
	}
	if len(request(t, s, "GET", "/api/contacts", bob, "", 200)["contacts"].([]any)) != 0 {
		t.Fatal("cross-account data leak")
	}
	request(t, s, "DELETE", "/api/contacts/profile?email=friend%40elsewhere.test", alice, "", 200)
	if len(request(t, s, "GET", "/api/contacts", alice, "", 200)["contacts"].([]any)) != 1 {
		t.Fatal("unsaving deleted membership")
	}
	request(t, s, "DELETE", "/api/contact-lists/"+a, alice, "", 200)
	book = request(t, s, "GET", "/api/contacts", alice, "", 200)["contacts"].([]any)
	if len(book) != 1 || book[0].(map[string]any)["saved"] != true || len(book[0].(map[string]any)["lists"].([]any)) != 0 {
		t.Fatal("list deletion erased contact", book)
	}
	request(t, s, "POST", "/api/contacts/profile", alice, `{"email":"friend@elsewhere.test","avatarSourcePreference":"auto|contact"}`, 400)
	request(t, s, "POST", "/api/contacts/profile", alice, `{"email":"friend@elsewhere.test","owner":"bob@example.test"}`, 400)
	request(t, s, "GET", "/api/contacts?q=%27%20OR%201%3D1", alice, "", 200)
}

func TestCSVImportAtomicAndQuoted(t *testing.T) {
	s := testService(t)
	token := login(t, s, "alice@example.test")
	input := map[string]any{"name": "CSV", "csvData": "email,name\r\nfriend@elsewhere.test,\"Friend, Someone\"\r\nfriend@elsewhere.test,duplicate\r\ninvalid-address,ignored\r\n", "columnMapping": map[string]string{"email": "email", "firstName": "name"}}
	body, _ := json.Marshal(input)
	result := request(t, s, "POST", "/api/contact-lists", token, string(body), 201)
	if result["imported"] != float64(1) {
		t.Fatal(result)
	}
	id := result["list"].(map[string]any)["id"].(string)
	contacts := request(t, s, "GET", "/api/contact-lists/"+id+"/contacts", token, "", 200)["contacts"].([]any)
	if contacts[0].(map[string]any)["firstName"] != "Friend, Someone" {
		t.Fatal(contacts)
	}
	input["csvData"] = "other,name\nmissing@email.test,X"
	body, _ = json.Marshal(input)
	request(t, s, "POST", "/api/contact-lists", token, string(body), 400)
	if len(request(t, s, "GET", "/api/contact-lists", token, "", 200)["lists"].([]any)) != 1 {
		t.Fatal("failed import left a partial list")
	}
	request(t, s, "PUT", "/api/contacts/lists", token, `{"email":"friend@elsewhere.test","listIds":[]}`, 200)
	if len(request(t, s, "GET", "/api/contact-lists/"+id+"/contacts", token, "", 200)["contacts"].([]any)) != 0 {
		t.Fatal("membership removal failed")
	}
}

func TestMigrationRestartAndDowngrade(t *testing.T) {
	dir := t.TempDir()
	db, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO contacts(owner,email,name) VALUES('a@example.test','b@example.test','Keep me')")
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	db, err = openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM contacts").Scan(&count); err != nil || count != 1 {
		t.Fatal("migration lost data", err)
	}
	_, err = db.Exec("INSERT INTO schema_version(version) VALUES(7)")
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if db, err = openStore(dir); err == nil {
		_ = db.Close()
		t.Fatal("allowed unsupported schema downgrade")
	}
}

func TestOriginsThrottleAndInputLimits(t *testing.T) {
	s := testService(t)
	r := httptest.NewRequest("POST", "/api/native/auth/login", strings.NewReader(`{"email":"a@example.test","password":"test-only-password"}`))
	r.Header.Set("Origin", "https://attacker.test")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("OPTIONS", "/api/contacts", nil)
	r.Header.Set("Origin", "https://localhost")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "https://localhost" {
		t.Fatal(w.Code)
	}
	for i := 0; i < 10; i++ {
		request(t, s, "POST", "/api/native/auth/login", "", `{"email":"a@example.test","password":"wrong"}`, 401)
	}
	request(t, s, "POST", "/api/native/auth/login", "", `{"email":"a@example.test","password":"test-only-password"}`, 429)
	request(t, s, "POST", "/api/native/auth/login", "", `{"email":"a@example.test","password":"test-only-password","imapHost":"localhost"}`, 400)
}

func TestMailboxAuthenticationCancelsStalledServer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buffer := make([]byte, 1024)
		for {
			if _, err = conn.Read(buffer); err != nil {
				return
			}
		}
	}()
	addr := listener.Addr().(*net.TCPAddr)
	auth := mailboxAuthenticator(Config{IMAPHost: "127.0.0.1", IMAPPort: addr.Port, IMAPSecurity: "implicit-tls"})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err = auth(ctx, "a@example.test", "not-stored"); err == nil {
		t.Fatal("stalled TLS authentication succeeded")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stalled connection did not close")
	}
}
