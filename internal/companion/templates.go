package companion

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

type mailTemplate struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Subject   string `json:"subject"`
	HTMLBody  string `json:"htmlBody"`
	CreatedAt string `json:"createdAt"`
}

func (s *service) templates(w http.ResponseWriter, r *http.Request, owner string) bool {
	path := r.URL.Path
	if path != "/api/templates" && !strings.HasPrefix(path, "/api/templates/") {
		return false
	}
	id := strings.TrimPrefix(path, "/api/templates")
	if id != "" {
		id = strings.TrimPrefix(id, "/")
		if len(id) != 64 || strings.Contains(id, "/") {
			problem(w, 404, "Template not found.")
			return true
		}
	}
	if r.Method == "GET" && id == "" {
		rows, err := s.db.QueryContext(r.Context(), "SELECT id,name,subject,html_body,created_at FROM templates WHERE owner=? ORDER BY created_at DESC,id", owner)
		if err != nil {
			problem(w, 503, "Could not load templates.")
			return true
		}
		defer rows.Close()
		items := []mailTemplate{}
		for rows.Next() {
			var item mailTemplate
			if err = rows.Scan(&item.ID, &item.Name, &item.Subject, &item.HTMLBody, &item.CreatedAt); err != nil {
				problem(w, 503, "Could not load templates.")
				return true
			}
			items = append(items, item)
		}
		if rows.Err() != nil {
			problem(w, 503, "Could not load templates.")
			return true
		}
		reply(w, 200, map[string]any{"templates": items})
		return true
	}
	if (r.Method == "POST" && id == "") || (r.Method == "PUT" && id != "") {
		var input struct {
			Name     string `json:"name"`
			Subject  string `json:"subject"`
			HTMLBody string `json:"htmlBody"`
		}
		if !readJSON(w, r, &input) {
			return true
		}
		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" || !validName(input.Name, 200) || strings.TrimSpace(input.Subject) == "" || !validName(input.Subject, 998) || strings.TrimSpace(input.HTMLBody) == "" || len(input.HTMLBody) > 128<<10 || !utf8.ValidString(input.HTMLBody) || strings.ContainsRune(input.HTMLBody, 0) {
			problem(w, 400, "Provide a name, subject and HTML body within the template size limits.")
			return true
		}
		item := mailTemplate{ID: id, Name: input.Name, Subject: input.Subject, HTMLBody: input.HTMLBody, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if id == "" {
			item.ID = opaqueID()
			result, err := s.db.ExecContext(r.Context(), "INSERT INTO templates(owner,id,name,subject,html_body,created_at) SELECT ?,?,?,?,?,? WHERE (SELECT count(*) FROM templates WHERE owner=?)<500", owner, item.ID, item.Name, item.Subject, item.HTMLBody, item.CreatedAt, owner)
			if err != nil {
				problem(w, 503, "Could not save template.")
				return true
			}
			count, _ := result.RowsAffected()
			if count == 0 {
				problem(w, 409, "Template limit reached (500 per mailbox).")
				return true
			}
			reply(w, 201, map[string]any{"template": item})
			return true
		}
		err := s.db.QueryRowContext(r.Context(), "UPDATE templates SET name=?,subject=?,html_body=? WHERE owner=? AND id=? RETURNING created_at", item.Name, item.Subject, item.HTMLBody, owner, id).Scan(&item.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, 404, "Template not found.")
			return true
		}
		if err != nil {
			problem(w, 503, "Could not save template.")
			return true
		}
		reply(w, 200, map[string]any{"template": item})
		return true
	}
	if r.Method == "DELETE" && id != "" {
		result, err := s.db.ExecContext(r.Context(), "DELETE FROM templates WHERE owner=? AND id=?", owner, id)
		if err != nil {
			problem(w, 503, "Could not delete template.")
			return true
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			problem(w, 404, "Template not found.")
			return true
		}
		reply(w, 200, map[string]bool{"success": true})
		return true
	}
	problem(w, 405, "Method not allowed.")
	return true
}
