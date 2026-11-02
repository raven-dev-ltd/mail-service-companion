package companion

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

type deliveryMessage struct {
	From         string   `json:"from"`
	FromName     string   `json:"fromName,omitempty"`
	ReplyTo      string   `json:"replyTo,omitempty"`
	To           []string `json:"to"`
	CC           []string `json:"cc,omitempty"`
	BCC          []string `json:"bcc,omitempty"`
	Subject      string   `json:"subject"`
	HTML         string   `json:"html"`
	Text         string   `json:"text"`
	InReplyTo    string   `json:"inReplyTo,omitempty"`
	References   []string `json:"references,omitempty"`
	ScheduledFor string   `json:"scheduledFor"`
}

func (s *service) deliveryConfigured() bool {
	return s.config.SMTPHost != "" && (s.config.SMTPMode == "service" || s.config.SMTPMode == "ip-allowlist")
}
func validateDelivery(owner string, m *deliveryMessage) error {
	if !validName(m.FromName, 200) || m.ReplyTo != "" && normalizedEmail(m.ReplyTo) == "" {
		return errors.New("Check your sender name and Reply-To address.")
	}
	if m.From == "" {
		m.From = owner
	}
	from, err := mail.ParseAddress(m.From)
	if err != nil || len(m.From) > 500 || !strings.EqualFold(from.Address, owner) || strings.ContainsAny(m.From, "\r\n") {
		return errors.New("Send only from your authenticated mailbox.")
	}
	if len(m.To) == 0 || len(m.To)+len(m.CC)+len(m.BCC) > 100 {
		return errors.New("Provide 1–100 recipients.")
	}
	for _, group := range [][]string{m.To, m.CC, m.BCC} {
		for _, address := range group {
			if normalizedEmail(address) == "" {
				return errors.New("Use valid recipient email addresses.")
			}
		}
	}
	if !validName(m.Subject, 200) || strings.TrimSpace(m.Subject) == "" || len(m.HTML)+len(m.Text) > 256<<10 {
		return errors.New("Provide a subject and a message within the 256 KiB limit.")
	}
	if len(m.References) > 50 {
		return errors.New("Too many reply references.")
	}
	for _, header := range append(m.References, m.InReplyTo) {
		if len(header) > 200 || strings.ContainsAny(header, "\r\n\x00") {
			return errors.New("Invalid reply headers.")
		}
	}
	return nil
}
func deliveryTime(value string) (time.Time, error) {
	if value == "" {
		return time.Now().UTC(), nil
	}
	date, err := time.Parse(time.RFC3339, value)
	if err != nil || date.Before(time.Now().Add(-time.Minute)) || date.After(time.Now().AddDate(1, 0, 0)) {
		return time.Time{}, errors.New("Choose a send time within the next year.")
	}
	return date, nil
}
func (s *service) delivery(w http.ResponseWriter, r *http.Request, owner string) bool {
	path := r.URL.Path
	if path != "/api/mail/scheduled" && path != "/api/mail/send" {
		return false
	}
	if path == "/api/mail/send" {
		if r.Method != "POST" {
			problem(w, 405, "Method not allowed.")
			return true
		}
		var m deliveryMessage
		if !readJSON(w, r, &m) {
			return true
		}
		if !s.deliveryConfigured() {
			problem(w, 503, "The operator has not configured background delivery.")
			return true
		}
		if m.ScheduledFor == "" {
			problem(w, 400, "Use your direct mail connection for immediate sending.")
			return true
		}
		if err := validateDelivery(owner, &m); err != nil {
			problem(w, 400, err.Error())
			return true
		}
		due, err := deliveryTime(m.ScheduledFor)
		if err != nil {
			problem(w, 400, err.Error())
			return true
		}
		if !due.After(time.Now()) {
			problem(w, 400, "Choose a future date and time.")
			return true
		}
		s.deliveryMu.Lock()
		defer s.deliveryMu.Unlock()
		payload, _ := json.Marshal(m)
		id := opaqueID()
		result, err := s.db.ExecContext(r.Context(), "INSERT INTO delivery_jobs(owner,id,payload,due,created_at) SELECT ?,?,?,?,? WHERE (SELECT count(*) FROM delivery_jobs WHERE owner=? AND status='PENDING')<1000", owner, id, string(payload), due.Unix(), time.Now().UTC().Format(time.RFC3339), owner)
		if err != nil {
			problem(w, 503, "Could not queue message.")
			return true
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			problem(w, 409, "Pending delivery limit reached (1000).")
			return true
		}
		reply(w, 201, map[string]any{"jobId": id, "scheduled": true, "undoDelay": 0})
		return true
	}
	switch r.Method {
	case "GET":
		rows, err := s.db.QueryContext(r.Context(), "SELECT id,payload,due,status,error,created_at,sent_at FROM delivery_jobs WHERE owner=? AND campaign_id='' ORDER BY due DESC LIMIT 1000", owner)
		if err != nil {
			problem(w, 503, "Could not load scheduled messages.")
			return true
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var id, payload, status, message, created, sent string
			var due int64
			if err = rows.Scan(&id, &payload, &due, &status, &message, &created, &sent); err != nil {
				problem(w, 503, "Could not load scheduled messages.")
				return true
			}
			var m deliveryMessage
			if json.Unmarshal([]byte(payload), &m) != nil {
				problem(w, 503, "Stored message is invalid.")
				return true
			}
			items = append(items, map[string]any{"id": id, "fromAddr": m.From, "toAddrs": m.To, "subject": m.Subject, "scheduledFor": time.Unix(due, 0).UTC().Format(time.RFC3339), "status": status, "errorMessage": message, "createdAt": created, "sentAt": sent})
		}
		if rows.Err() != nil {
			problem(w, 503, "Could not load scheduled messages.")
			return true
		}
		reply(w, 200, map[string]any{"scheduled": items})
	case "DELETE":
		var input struct {
			ID string `json:"id"`
		}
		if !readJSON(w, r, &input) {
			return true
		}
		if input.ID == "" || len(input.ID) > 128 {
			problem(w, 400, "A scheduled message ID is required.")
			return true
		}
		// A claimed message cannot be recalled, even if SMTP is still in progress.
		result, err := s.db.ExecContext(r.Context(), "UPDATE delivery_jobs SET status='CANCELLED' WHERE owner=? AND id=? AND campaign_id='' AND status IN ('PENDING','CANCELLED')", owner, input.ID)
		if err != nil {
			problem(w, 503, "Could not cancel message.")
			return true
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			var status string
			err = s.db.QueryRowContext(r.Context(), "SELECT status FROM delivery_jobs WHERE owner=? AND id=? AND campaign_id=''", owner, input.ID).Scan(&status)
			if err != sql.ErrNoRows {
				if err != nil {
					problem(w, 503, "Could not check cancellation.")
				} else {
					problem(w, 409, "This message has already been submitted; it cannot be recalled.")
				}
				return true
			}
			// Already absent is safe to acknowledge, without leaking another owner.
		}
		reply(w, 200, map[string]any{"success": true, "status": "CANCELLED"})
	default:
		problem(w, 405, "Method not allowed.")
	}
	return true
}

// A single bounded sender, with an atomic claim, survives restarts without
// replaying an SMTP submission whose acknowledgement may have been lost.
func (s *service) runDelivery(ctx context.Context) {
	if !s.deliveryConfigured() {
		return
	}
	_, err := s.db.ExecContext(ctx, "UPDATE delivery_jobs SET status='FAILED',error='Delivery outcome uncertain after restart. Check Sent/recipient before sending again.' WHERE status='SENDING'")
	if err != nil {
		return
	}
	interval := s.config.SendInterval
	if interval < 5*time.Second {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.deliverOne(ctx)
		}
	}
}
func (s *service) deliverOne(ctx context.Context) {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	var owner, id, payload string
	err := s.db.QueryRowContext(ctx, "UPDATE delivery_jobs SET status='SENDING' WHERE rowid=(SELECT rowid FROM delivery_jobs WHERE status='PENDING' AND due<=? ORDER BY due,rowid LIMIT 1) AND status='PENDING' RETURNING owner,id,payload", time.Now().Unix()).Scan(&owner, &id, &payload)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return
	}
	status, note, sent := "FAILED", "", ""
	var m deliveryMessage
	if !s.deliveryConfigured() {
		note = "Administrator relay is unavailable."
	} else if json.Unmarshal([]byte(payload), &m) != nil || validateDelivery(owner, &m) != nil {
		note = "Stored message is invalid."
	} else {
		sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = s.sendDelivery(sendCtx, owner, m, id)
		cancel()
		if err == nil {
			status = "SENT"
			sent = time.Now().UTC().Format(time.RFC3339)
		} else {
			note = "SMTP submission failed or its outcome is uncertain. Check the recipient before sending again; no automatic retry was made."
		}
	}
	// Finish even after shutdown cancellation, so acknowledged sends stay sent.
	finalCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.db.ExecContext(finalCtx, "UPDATE delivery_jobs SET status=?,error=?,sent_at=? WHERE owner=? AND id=? AND status='SENDING'", status, note, sent, owner, id)
}
