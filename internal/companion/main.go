package companion

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

type service struct {
	routing      *routingConfig
	push         *pushSender
	db           *sql.DB
	config       Config
	authenticate func(context.Context, string, string) error
	mu           sync.Mutex
	attempts     map[string]attempt
	loginSlots   chan struct{}
	deliveryMu   sync.Mutex
	sendDelivery func(context.Context, string, deliveryMessage, string) error
}
type attempt struct {
	count int
	until time.Time
}

func openStore(directory string) (*sql.DB, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "companion.sqlite")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*sql.DB, error) { _ = db.Close(); return nil, err }
	if _, err = db.Exec("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000; PRAGMA secure_delete=ON;"); err != nil {
		return fail(err)
	}
	var exists int
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_version'").Scan(&exists); err != nil {
		return fail(err)
	}
	var version int
	if exists > 0 {
		if err = db.QueryRow("SELECT coalesce(max(version),0) FROM schema_version").Scan(&version); err != nil {
			return fail(err)
		}
		if version > 7 {
			return fail(errors.New("database schema is newer than this binary; refusing downgrade"))
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return fail(err)
	}
	if version > 0 && version < 6 {
		if _, err = tx.Exec("DROP TABLE IF EXISTS delivery_credentials; DROP TABLE IF EXISTS push_registrations"); err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
	}
	if _, err = tx.Exec(schema); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if version > 0 && version < 6 {
		if _, err = tx.Exec("UPDATE delivery_jobs SET status='FAILED',error='Relay migration: renew consent and reschedule.' WHERE status IN ('PENDING','SENDING')"); err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	if version > 0 && version < 6 {
		if _, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			return fail(err)
		}
	}
	return db, nil
}

func newService(db *sql.DB, c Config, auth func(context.Context, string, string) error) *service {
	s := &service{db: db, config: c, authenticate: auth, attempts: map[string]attempt{}, loginSlots: make(chan struct{}, 8)}
	s.sendDelivery = smtpDelivery(c)
	return s
}

func Run() {
	config, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	db, err := openStore(config.DataDir)
	if err != nil {
		log.Fatal("could not initialize companion storage: ", err)
	}
	defer db.Close()
	app := newService(db, config, mailboxAuthenticator(config))
	app.push, err = newPushSender(context.Background(), os.Getenv("COMPANION_FCM_CREDENTIALS"))
	if err != nil {
		log.Fatal(err)
	}
	if err = app.configureRouting(context.Background()); err != nil {
		log.Fatal("could not generate feature routing: ", err)
	}
	if len(os.Args) == 2 && os.Args[1] == "--configure" {
		log.Print("Generated feature-routing.json; no HTTP listener or workers started.")
		return
	}
	server := &http.Server{Addr: config.Listen, Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); app.runDelivery(ctx) }()
	pushDone := make(chan struct{})
	go func() { defer close(pushDone); app.runPush(ctx) }()
	defer func() { stop(); <-pushDone }()
	defer func() { stop(); <-workerDone }()
	go func() {
		<-ctx.Done()
		deadline, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(deadline)
	}()
	log.Print("Companion ready; schema initialized automatically. Configure HTTPS at your reverse proxy.")
	if err = server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func problem(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]string{"error": message})
}
func readJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		problem(w, 415, "Send JSON with Content-Type: application/json.")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		problem(w, 400, "Invalid or oversized request.")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		problem(w, 400, "Send one JSON object.")
		return false
	}
	return true
}
func normalizedEmail(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || len(value) > 254 || strings.ContainsAny(value, "\r\n ") || !strings.Contains(value, "@") {
		return ""
	}
	return value
}
func opaqueID() string {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		panic("secure random source failed")
	}
	return hex.EncodeToString(value)
}
func tokenHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (s *service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if origin := r.Header.Get("Origin"); origin != "" {
		allowed := false
		for _, candidate := range s.config.Origins {
			if candidate == origin {
				allowed = true
				break
			}
		}
		if !allowed {
			problem(w, 403, "Origin is not allowed.")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
	}
	if r.URL.Path == "/healthz" && r.Method == "GET" {
		if err := s.db.PingContext(r.Context()); err != nil {
			problem(w, 503, "Service is unavailable.")
		} else {
			reply(w, 200, map[string]bool{"ok": true})
		}
		return
	}
	if r.URL.Path == "/api/native/auth/login" && r.Method == "POST" {
		s.login(w, r)
		return
	}
	if r.URL.Path == "/api/integrations/mail-events" {
		s.mailEvent(w, r)
		return
	}
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") || len(header) != 71 {
		problem(w, 401, "Sign in to the companion service.")
		return
	}
	token := strings.TrimPrefix(header, "Bearer ")
	var owner string
	var expires int64
	err := s.db.QueryRowContext(r.Context(), "SELECT owner,expires FROM sessions WHERE token_hash=? AND expires>?", tokenHash(token), time.Now().Unix()).Scan(&owner, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 401, "The companion session has expired.")
		return
	}
	if err != nil {
		problem(w, 503, "Service is unavailable.")
		return
	}
	switch r.URL.Path {
	case "/api/native/auth/session":
		if r.Method == "GET" {
			reply(w, 200, sessionResponse(owner, expires, ""))
			return
		}
	case "/api/native/auth/logout":
		if r.Method == "POST" {
			if _, err = s.db.ExecContext(r.Context(), "DELETE FROM sessions WHERE token_hash=?", tokenHash(token)); err != nil {
				problem(w, 503, "Could not revoke session.")
			} else {
				reply(w, 200, map[string]bool{"success": true})
			}
			return
		}
	case "/api/native/capabilities":
		if r.Method == "GET" {
			plan := s.featureRouting()
			features := map[string]any{}
			for id, route := range plan.Features {
				if route.Source != "companion" {
					continue
				}
				if id == "scheduledSend" || id == "snooze" {
					features[id] = map[string]any{"supported": route.Enabled, "serverExecution": route.ServerExecution}
				} else {
					features[id] = route.Enabled
				}
			}
			reply(w, 200, map[string]any{"schemaVersion": 1, "adapter": "companion-v1", "deliveryVersion": 1, "pushVersion": 2, "labelsVersion": 1, "profileVersion": 1, "routing": plan, "features": features})
			return
		}
	default:
		if s.dispatchFeature(w, r, owner) {
			return
		}
		if s.deliveryConsent(w, r, owner) {
			return
		}
	}
	problem(w, 404, "This companion resource is not available.")
}

func sessionResponse(email string, expires int64, token string) map[string]any {
	result := map[string]any{"user": map[string]string{"email": email, "name": strings.Split(email, "@")[0]}, "expires": time.Unix(expires, 0).UTC().Format(time.RFC3339)}
	if token != "" {
		result["accessToken"] = token
	}
	return result
}
func (s *service) allowLogin(key string, limit int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if len(s.attempts) >= 4096 {
		for candidate, record := range s.attempts {
			if now.After(record.until) {
				delete(s.attempts, candidate)
			}
		}
		if len(s.attempts) >= 4096 {
			return false
		}
	}
	record := s.attempts[key]
	if now.After(record.until) {
		record = attempt{until: now.Add(5 * time.Minute)}
	}
	if record.count >= limit {
		return false
	}
	record.count++
	s.attempts[key] = record
	return true
}
func (s *service) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email      string `json:"email"`
		Password   string `json:"password"`
		RememberMe bool   `json:"rememberMe"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	email := normalizedEmail(input.Email)
	allowed := false
	if email != "" {
		for _, domain := range s.config.Domains {
			if strings.Split(email, "@")[1] == domain {
				allowed = true
			}
		}
	}
	if !allowed || input.Password == "" || len(input.Password) > 4096 {
		problem(w, 401, "Mailbox sign-in failed.")
		return
	}
	remote, _, _ := net.SplitHostPort(r.RemoteAddr)
	// Never trust forwarded headers from callers; enforce proxy rate limits too.
	if !s.allowLogin("account:"+email, 10) || !s.allowLogin("ip:"+remote, 300) {
		w.Header().Set("Retry-After", "300")
		problem(w, 429, "Too many sign-in attempts. Try again shortly.")
		return
	}
	select {
	case s.loginSlots <- struct{}{}:
		defer func() { <-s.loginSlots }()
	default:
		problem(w, 503, "Sign-in is busy. Try again shortly.")
		return
	}
	if err := s.authenticate(r.Context(), email, input.Password); err != nil {
		problem(w, 401, "Mailbox sign-in failed. Check credentials and the configured mail server.")
		return
	}
	input.Password = ""
	s.mu.Lock()
	delete(s.attempts, "account:"+email)
	s.mu.Unlock()
	token := opaqueID()
	expires := time.Now().Add(8 * time.Hour).Unix()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "Service is unavailable.")
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM sessions WHERE expires<=?", time.Now().Unix()); err == nil {
		_, err = tx.Exec("DELETE FROM sessions WHERE token_hash IN (SELECT token_hash FROM sessions WHERE owner=? ORDER BY expires DESC LIMIT -1 OFFSET 19)", email)
	}
	if err == nil {
		_, err = tx.Exec("INSERT INTO sessions(token_hash,owner,expires) VALUES(?,?,?)", tokenHash(token), email, expires)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(w, 503, "Could not create session.")
		return
	}
	reply(w, 200, sessionResponse(email, expires, token))
}
