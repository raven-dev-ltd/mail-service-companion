package companion

import (
	"database/sql"
	"encoding/json"
	"html"
	"net/http"
	"strings"
	"time"
)

func personalize(value, name, email string, markup bool) string {
	first := name
	if parts := strings.Fields(name); len(parts) > 0 {
		first = parts[0]
	}
	if markup {
		name = html.EscapeString(name)
		first = html.EscapeString(first)
		email = html.EscapeString(email)
	}
	return strings.NewReplacer("{{name}}", name, "{{first_name}}", first, "{{email}}", email).Replace(value)
}
func (s *service) campaigns(w http.ResponseWriter, r *http.Request, owner string) bool {
	if r.URL.Path != "/api/campaigns" && !strings.HasPrefix(r.URL.Path, "/api/campaigns/") {
		return false
	}
	id := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/campaigns"), "/")
	if r.Method == "GET" {
		if id != "" {
			var exists int
			if s.db.QueryRowContext(r.Context(), "SELECT count(*) FROM campaigns WHERE owner=? AND id=?", owner, id).Scan(&exists) != nil || exists != 1 {
				problem(w, 404, "Campaign not found.")
				return true
			}
			rows, err := s.db.QueryContext(r.Context(), "SELECT id,payload,status,error,sent_at FROM delivery_jobs WHERE owner=? AND campaign_id=? ORDER BY rowid LIMIT 1000", owner, id)
			if err != nil {
				problem(w, 503, "Could not load recipients.")
				return true
			}
			defer rows.Close()
			items := []map[string]any{}
			for rows.Next() {
				var job, payload, status, note, sent string
				if rows.Scan(&job, &payload, &status, &note, &sent) != nil {
					problem(w, 503, "Could not read recipients.")
					return true
				}
				var m deliveryMessage
				if json.Unmarshal([]byte(payload), &m) != nil {
					problem(w, 503, "Invalid stored message.")
					return true
				}
				items = append(items, map[string]any{"id": job, "to": m.To, "status": status, "error": note, "sentAt": sent})
			}
			if rows.Err() != nil {
				problem(w, 503, "Could not read recipients.")
				return true
			}
			reply(w, 200, map[string]any{"recipients": items})
			return true
		}
		rows, err := s.db.QueryContext(r.Context(), `SELECT c.id,c.name,c.status,c.created_at,c.scheduled_at,c.template_name,c.subject,
   count(j.id),coalesce(sum(j.status='PENDING'),0),coalesce(sum(j.status='SENDING'),0),coalesce(sum(j.status='FAILED'),0)
   FROM campaigns c LEFT JOIN delivery_jobs j ON j.owner=c.owner AND j.campaign_id=c.id WHERE c.owner=?
   GROUP BY c.id ORDER BY c.created_at DESC LIMIT 500`, owner)
		if err != nil {
			problem(w, 503, "Could not list campaigns.")
			return true
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var id, name, status, created, tname, subject string
			var scheduled sql.NullString
			var count, pending, sending, failed int
			if rows.Scan(&id, &name, &status, &created, &scheduled, &tname, &subject, &count, &pending, &sending, &failed) != nil {
				problem(w, 503, "Could not read campaigns.")
				return true
			}
			if status != "DRAFT" && status != "CANCELLED" {
				if pending+sending == 0 {
					status = "COMPLETED"
					if failed > 0 {
						status = "COMPLETED_WITH_ERRORS"
					}
				} else if sending > 0 {
					status = "SENDING"
				} else {
					status = "SCHEDULED"
				}
			}
			var date any
			if scheduled.Valid {
				date = scheduled.String
			}
			items = append(items, map[string]any{"id": id, "name": name, "status": status, "createdAt": created, "startedAt": nil, "scheduledAt": date, "template": map[string]string{"name": tname, "subject": subject}, "_count": map[string]int{"sends": count}})
		}
		if rows.Err() != nil {
			problem(w, 503, "Could not read campaigns.")
			return true
		}
		reply(w, 200, map[string]any{"campaigns": items})
		return true
	}
	if r.Method == "DELETE" && id != "" {
		s.deliveryMu.Lock()
		defer s.deliveryMu.Unlock()
		tx, err := s.db.BeginTx(r.Context(), nil)
		if err != nil {
			problem(w, 503, "Could not cancel campaign.")
			return true
		}
		defer tx.Rollback()
		result, err := tx.Exec("DELETE FROM campaigns WHERE owner=? AND id=?", owner, id)
		if err != nil {
			problem(w, 503, "Could not cancel campaign.")
			return true
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			problem(w, 404, "Campaign not found.")
			return true
		}
		if _, err = tx.Exec("DELETE FROM delivery_jobs WHERE owner=? AND campaign_id=?", owner, id); err != nil {
			problem(w, 503, "Could not cancel campaign.")
			return true
		}
		if tx.Commit() != nil {
			problem(w, 503, "Could not cancel campaign.")
			return true
		}
		reply(w, 200, map[string]bool{"success": true})
		return true
	}
	if r.Method != "POST" || id != "" {
		problem(w, 405, "Method not allowed.")
		return true
	}
	var input struct {
		Action      string  `json:"action"`
		CampaignID  string  `json:"campaignId"`
		Name        string  `json:"name"`
		TemplateID  string  `json:"templateId"`
		ListID      string  `json:"listId"`
		ScheduledAt *string `json:"scheduledAt"`
	}
	if !readJSON(w, r, &input) {
		return true
	}
	if !s.deliveryConfigured() {
		problem(w, 503, "The operator has not configured campaign delivery.")
		return true
	}
	if input.Action == "" {
		if strings.TrimSpace(input.Name) == "" || !validName(input.Name, 200) {
			problem(w, 400, "Provide a campaign name.")
			return true
		}
		id = opaqueID()
		result, err := s.db.ExecContext(r.Context(), `INSERT INTO campaigns(owner,id,name,template_id,list_id,template_name,subject,created_at,scheduled_at)
   SELECT ?,?,?,t.id,l.id,t.name,t.subject,?,? FROM templates t JOIN lists l ON l.owner=t.owner
   WHERE t.owner=? AND t.id=? AND l.id=? AND (SELECT count(*) FROM campaigns WHERE owner=?)<500`,
			owner, id, input.Name, time.Now().UTC().Format(time.RFC3339), input.ScheduledAt, owner, input.TemplateID, input.ListID, owner)
		if err != nil {
			problem(w, 503, "Could not create campaign.")
			return true
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			problem(w, 400, "Choose your own template and list, and keep fewer than 500 campaigns.")
			return true
		}
		reply(w, 201, map[string]any{"campaign": map[string]string{"id": id}})
		return true
	}
	if input.Action != "launch" {
		problem(w, 400, "Unknown campaign action.")
		return true
	}
	dueValue := ""
	if input.ScheduledAt != nil {
		dueValue = *input.ScheduledAt
	}
	due, err := deliveryTime(dueValue)
	if err != nil {
		problem(w, 400, err.Error())
		return true
	}
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "Could not queue campaign.")
		return true
	}
	defer tx.Rollback()
	var listID, subject, body, status, templateName string
	err = tx.QueryRow(`SELECT c.list_id,t.subject,t.html_body,c.status,t.name FROM campaigns c JOIN templates t ON t.owner=c.owner AND t.id=c.template_id WHERE c.owner=? AND c.id=?`, owner, input.CampaignID).Scan(&listID, &subject, &body, &status, &templateName)
	if err != nil {
		problem(w, 404, "Campaign or template not found.")
		return true
	}
	if status != "DRAFT" {
		problem(w, 409, "Campaign has already been queued; it will not be launched twice.")
		return true
	}
	rows, err := tx.Query("SELECT c.email,c.name FROM contacts c JOIN memberships m ON m.owner=c.owner AND m.email=c.email WHERE m.owner=? AND m.list_id=? ORDER BY c.email LIMIT 1001", owner, listID)
	if err != nil {
		problem(w, 503, "Could not read the contact list.")
		return true
	}
	type recipient struct{ email, name string }
	people := []recipient{}
	for rows.Next() {
		var p recipient
		if err = rows.Scan(&p.email, &p.name); err != nil {
			break
		}
		people = append(people, p)
	}
	rowsErr := rows.Err()
	rows.Close()
	if err != nil || rowsErr != nil {
		problem(w, 503, "Could not read the contact list.")
		return true
	}
	var pending int
	if tx.QueryRow("SELECT count(*) FROM delivery_jobs WHERE owner=? AND status='PENDING'", owner).Scan(&pending) != nil {
		problem(w, 503, "Could not inspect queue.")
		return true
	}
	if len(people) == 0 || len(people)+pending > 1000 {
		problem(w, 400, "Use a nonempty list; at most 1000 pending deliveries per mailbox are allowed.")
		return true
	}
	for _, p := range people {
		m := deliveryMessage{From: owner, To: []string{p.email}, Subject: personalize(subject, p.name, p.email, false), HTML: personalize(body, p.name, p.email, true), ScheduledFor: due.Format(time.RFC3339)}
		if validateDelivery(owner, &m) != nil {
			problem(w, 400, "A personalized recipient or message is invalid.")
			return true
		}
		payload, _ := json.Marshal(m)
		if _, err = tx.Exec("INSERT INTO delivery_jobs(owner,id,campaign_id,payload,due,created_at) VALUES(?,?,?,?,?,?)", owner, opaqueID(), input.CampaignID, string(payload), due.Unix(), time.Now().UTC().Format(time.RFC3339)); err != nil {
			problem(w, 503, "Could not queue campaign.")
			return true
		}
	}
	if _, err = tx.Exec("UPDATE campaigns SET status='SCHEDULED',scheduled_at=?,template_name=?,subject=? WHERE owner=? AND id=?", due.Format(time.RFC3339), templateName, subject, owner, input.CampaignID); err != nil {
		problem(w, 503, "Could not queue campaign.")
		return true
	}
	if tx.Commit() != nil {
		problem(w, 503, "Could not queue campaign.")
		return true
	}
	reply(w, 200, map[string]any{"success": true, "recipients": len(people)})
	return true
}
