package companion

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type mailEventInput struct {
	ID       string `json:"id"`
	Mailbox  string `json:"mailbox"`
	Validity uint32 `json:"validity"`
	LastUID  uint32 `json:"lastUid"`
}

// Signature covers the timestamp and exact body; durable IDs prevent replay.
func (s *service) mailEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		problem(w, 405, "Method not allowed.")
		return
	}
	if !s.pushConfigured() {
		problem(w, 503, "Event push is not configured.")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	if err != nil {
		problem(w, 413, "Event too large.")
		return
	}
	timestamp := r.Header.Get("X-Companion-Timestamp")
	stamp, err := strconv.ParseInt(timestamp, 10, 64)
	now := time.Now().Unix()
	signature, decodeErr := hex.DecodeString(r.Header.Get("X-Companion-Signature"))
	mac := hmac.New(sha256.New, s.config.EventSecret)
	mac.Write([]byte(timestamp + "."))
	mac.Write(raw)
	if err != nil || decodeErr != nil || stamp < now-300 || stamp > now+300 || !hmac.Equal(signature, mac.Sum(nil)) {
		problem(w, 401, "Invalid event signature or timestamp.")
		return
	}
	var event mailEventInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&event) != nil || decoder.Decode(new(any)) != io.EOF || len(event.ID) < 16 || len(event.ID) > 128 || event.Validity == 0 || event.LastUID == 0 {
		problem(w, 400, "Invalid INBOX delivery event.")
		return
	}
	owner := normalizedEmail(event.Mailbox)
	allowed := false
	for _, domain := range s.config.Domains {
		if owner != "" && strings.HasSuffix(owner, "@"+domain) {
			allowed = true
		}
	}
	if !allowed {
		problem(w, 400, "Mailbox domain is not allowed.")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "Event queue unavailable.")
		return
	}
	defer tx.Rollback()
	digest := tokenHash(string(raw))
	// Purge expired work even when the worker is idle.
	if _, err = tx.Exec("DELETE FROM mail_events WHERE expires<=?; DELETE FROM push_jobs WHERE expires<=?", now, now); err != nil {
		problem(w, 503, "Event queue unavailable.")
		return
	}
	var previous string
	if err = tx.QueryRow("SELECT digest FROM mail_events WHERE id=?", event.ID).Scan(&previous); err == nil {
		if previous != digest {
			problem(w, 409, "Event ID reused with different content.")
			return
		}
		reply(w, 202, map[string]bool{"accepted": true})
		return
	}
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM mail_events").Scan(&count); err != nil || count >= 10000 {
		problem(w, 503, "Event queue is full.")
		return
	}
	if _, err = tx.Exec("INSERT INTO mail_events(id,digest,expires) VALUES(?,?,?)", event.ID, digest, now+86400); err != nil {
		problem(w, 503, "Could not queue event.")
		return
	}
	_, err = tx.Exec("INSERT INTO push_jobs(event_id,owner,registration_id,validity,last_uid,due,expires) SELECT ?,owner,id,?,?,?,? FROM push_registrations WHERE owner=? AND expires>?", event.ID, event.Validity, event.LastUID, now, now+300, owner, now)
	if err != nil || tx.Commit() != nil {
		problem(w, 503, "Could not queue event.")
		return
	}
	reply(w, 202, map[string]bool{"accepted": true})
}

func (s *service) runPush(ctx context.Context) {
	if !s.pushConfigured() {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pushBatch(ctx)
		}
	}
}
func (s *service) pushBatch(ctx context.Context) {
	now := time.Now().Unix()
	_, _ = s.db.ExecContext(ctx, "DELETE FROM push_registrations WHERE expires<=?; DELETE FROM push_jobs WHERE expires<=? OR attempts>=5; DELETE FROM mail_events WHERE expires<=?", now, now, now)
	// One worker, at most four sends per tick. Retries expire after five minutes.
	for i := 0; i < 4; i++ {
		var event, owner, id, token string
		var validity, last uint32
		var attempts int
		err := s.db.QueryRowContext(ctx, `SELECT j.event_id,j.owner,j.registration_id,r.token,j.validity,j.last_uid,j.attempts FROM push_jobs j JOIN push_registrations r ON r.owner=j.owner AND r.id=j.registration_id WHERE j.due<=? AND j.expires>? AND r.expires>? ORDER BY j.due LIMIT 1`, now, now, now).Scan(&event, &owner, &id, &token, &validity, &last, &attempts)
		if err != nil {
			return
		}
		sendCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		err = s.push.send(sendCtx, token, id, validity, last)
		cancel()
		if err == nil {
			_, _ = s.db.ExecContext(ctx, "DELETE FROM push_jobs WHERE event_id=? AND owner=? AND registration_id=?", event, owner, id)
		} else {
			_, _ = s.db.ExecContext(ctx, "UPDATE push_jobs SET attempts=attempts+1,due=? WHERE event_id=? AND owner=? AND registration_id=?", now+int64(15*(1<<attempts)), event, owner, id)
		}
	}
}
