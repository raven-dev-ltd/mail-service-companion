package companion

import (
	"net/http"
	"regexp"
	"strings"
)

var labelColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func (s *service) labels(w http.ResponseWriter, r *http.Request, owner string) bool {
	if r.URL.Path != "/api/labels" && !strings.HasPrefix(r.URL.Path, "/api/labels/") {
		return false
	}
	id := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/labels"), "/")
	switch {
	case r.Method == "GET" && id == "":
		rows, err := s.db.QueryContext(r.Context(), "SELECT id,name,color FROM labels WHERE owner=? ORDER BY name", owner)
		if err != nil {
			problem(w, 503, "Could not load labels.")
			return true
		}
		defer rows.Close()
		items := []map[string]string{}
		for rows.Next() {
			var id, name, color string
			if rows.Scan(&id, &name, &color) != nil {
				problem(w, 503, "Could not read labels.")
				return true
			}
			items = append(items, map[string]string{"id": id, "name": name, "color": color, "keyword": "mm_" + id})
		}
		if rows.Err() != nil {
			problem(w, 503, "Could not read labels.")
			return true
		}
		reply(w, 200, map[string]any{"labels": items})
	case (r.Method == "POST" && id == "") || (r.Method == "PUT" && id != ""):
		var input struct {
			Name  string `json:"name"`
			Color string `json:"color"`
		}
		if !readJSON(w, r, &input) {
			return true
		}
		input.Name = strings.TrimSpace(input.Name)
		if input.Color == "" {
			input.Color = "#3b82f6"
		}
		if input.Name == "" || !validName(input.Name, 80) || !labelColor.MatchString(input.Color) {
			problem(w, 400, "Provide a label name and six-digit hex color.")
			return true
		}
		if id == "" {
			id = opaqueID()
			result, err := s.db.ExecContext(r.Context(), "INSERT INTO labels(owner,id,name,color) SELECT ?,?,?,? WHERE (SELECT count(*) FROM labels WHERE owner=?)<100", owner, id, input.Name, input.Color, owner)
			if err != nil {
				problem(w, 409, "A label with that name may already exist.")
				return true
			}
			n, _ := result.RowsAffected()
			if n != 1 {
				problem(w, 409, "Label limit reached (100).")
				return true
			}
		} else {
			result, err := s.db.ExecContext(r.Context(), "UPDATE labels SET name=?,color=? WHERE owner=? AND id=?", input.Name, input.Color, owner, id)
			if err != nil {
				problem(w, 409, "Could not rename label; use a unique name.")
				return true
			}
			n, _ := result.RowsAffected()
			if n != 1 {
				problem(w, 404, "Label not found.")
				return true
			}
		}
		reply(w, 200, map[string]any{"label": map[string]string{"id": id, "name": input.Name, "color": input.Color, "keyword": "mm_" + id}})
	case r.Method == "DELETE" && id != "":
		result, err := s.db.ExecContext(r.Context(), "DELETE FROM labels WHERE owner=? AND id=?", owner, id)
		if err != nil {
			problem(w, 503, "Could not delete label.")
			return true
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			problem(w, 404, "Label not found.")
			return true
		}
		reply(w, 200, map[string]bool{"success": true})
	default:
		problem(w, 405, "Method not allowed.")
	}
	return true
}
