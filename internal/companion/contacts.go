package companion

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func validName(value string, max int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= max && !strings.ContainsAny(value, "\r\n\x00")
}
func validAvatar(value string) bool {
	if value == "" {
		return true
	}
	parts := strings.SplitN(value, ",", 2)
	if len(parts) != 2 || (parts[0] != "data:image/png;base64" && parts[0] != "data:image/jpeg;base64") || len(value) > 700000 {
		return false
	}
	data, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil || len(data) > 512<<10 {
		return false
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 2048 || config.Height > 2048 || parts[0] != "data:image/"+format+";base64" {
		return false
	}
	_, _, err = image.Decode(bytes.NewReader(data))
	return err == nil
}

func (s *service) contacts(w http.ResponseWriter, r *http.Request, owner string) bool {
	switch r.URL.Path {
	case "/api/contacts":
		if r.Method == "GET" {
			s.addressBook(w, r, owner, false)
			return true
		}
	case "/api/contacts/suggest":
		if r.Method == "GET" {
			s.addressBook(w, r, owner, true)
			return true
		}
	case "/api/contacts/profile":
		s.profile(w, r, owner)
		return true
	case "/api/contacts/lists":
		s.memberships(w, r, owner)
		return true
	case "/api/contact-lists":
		s.lists(w, r, owner)
		return true
	case "/api/avatars/image":
		if r.Method == "GET" && r.URL.Query().Get("source") == "contact" {
			s.avatar(w, r, owner)
			return true
		}
	}
	if tail, ok := strings.CutPrefix(r.URL.Path, "/api/contact-lists/"); ok {
		id := strings.TrimSuffix(tail, "/contacts")
		if len(id) != 64 || strings.Contains(id, "/") {
			problem(w, 404, "Contact list not found.")
			return true
		}
		if strings.HasSuffix(tail, "/contacts") && r.Method == "GET" {
			s.listContacts(w, r, owner, id)
		} else {
			s.changeList(w, r, owner, id)
		}
		return true
	}
	return false
}

func (s *service) addressBook(w http.ResponseWriter, r *http.Request, owner string, suggest bool) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	after := r.URL.Query().Get("after")
	if len(q) > 200 || len(after) > 254 {
		problem(w, 400, "Invalid contact search.")
		return
	}
	limit := 100
	if suggest {
		limit = 10
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 100 {
				problem(w, 400, "Invalid suggestion limit.")
				return
			}
			limit = n
		}
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT c.email,c.name,c.saved,l.name FROM
 (SELECT * FROM contacts WHERE owner=? AND email>? AND
 (saved=1 OR EXISTS(SELECT 1 FROM memberships m WHERE m.owner=contacts.owner AND m.email=contacts.email))
 AND (instr(lower(email),lower(?))>0 OR instr(lower(name),lower(?))>0)
 ORDER BY email LIMIT ?) c
 LEFT JOIN memberships m ON m.owner=c.owner AND m.email=c.email
 LEFT JOIN lists l ON l.owner=m.owner AND l.id=m.list_id ORDER BY c.email,l.name`, owner, after, q, q, limit+1)
	if err != nil {
		problem(w, 503, "Could not load contacts.")
		return
	}
	defer rows.Close()
	contacts := []map[string]any{}
	for rows.Next() {
		var email, name string
		var saved bool
		var list sql.NullString
		if err = rows.Scan(&email, &name, &saved, &list); err != nil {
			problem(w, 503, "Could not load contacts.")
			return
		}
		if len(contacts) == 0 || contacts[len(contacts)-1]["email"] != email {
			if name == "" {
				name = email
			}
			contacts = append(contacts, map[string]any{"email": email, "name": name, "saved": saved, "lists": []string{}})
		}
		if list.Valid {
			entry := contacts[len(contacts)-1]
			entry["lists"] = append(entry["lists"].([]string), list.String)
		}
	}
	if rows.Err() != nil {
		problem(w, 503, "Could not load contacts.")
		return
	}
	var cursor any
	if len(contacts) > limit {
		contacts = contacts[:limit]
		cursor = contacts[len(contacts)-1]["email"]
	}
	if suggest {
		for _, contact := range contacts {
			contact["contactEmail"] = contact["email"]
			contact["displayName"] = contact["name"]
			contact["isSaved"] = contact["saved"]
		}
	}
	reply(w, 200, map[string]any{"contacts": contacts, "nextCursor": cursor})
}

func (s *service) profile(w http.ResponseWriter, r *http.Request, owner string) {
	email := normalizedEmail(r.URL.Query().Get("email"))
	if r.Method == "POST" {
		var input struct {
			Email  string  `json:"email"`
			Name   *string `json:"name"`
			Avatar *string `json:"avatarDataUrl"`
			Source *string `json:"avatarSourcePreference"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		email = normalizedEmail(input.Email)
		if email == "" || input.Name != nil && !validName(*input.Name, 200) || input.Avatar != nil && !validAvatar(*input.Avatar) {
			problem(w, 400, "Invalid contact name or photo. Use a PNG/JPEG photo up to 512 KB and 2048 pixels.")
			return
		}
		if input.Source != nil && !map[string]bool{"auto": true, "contact": true, "bimi": true, "gravatar": true, "domain": true, "identicon": true}[*input.Source] {
			problem(w, 400, "Invalid photo source.")
			return
		}
		tx, err := s.db.BeginTx(r.Context(), nil)
		if err != nil {
			problem(w, 503, "Could not save contact.")
			return
		}
		defer tx.Rollback()
		if err = ensureContact(tx, owner, email, ""); err == nil {
			_, err = tx.Exec("UPDATE contacts SET saved=1 WHERE owner=? AND email=?", owner, email)
		}
		if err == nil && input.Name != nil {
			_, err = tx.Exec("UPDATE contacts SET name=? WHERE owner=? AND email=?", strings.TrimSpace(*input.Name), owner, email)
		}
		if err == nil && input.Avatar != nil {
			source := "contact"
			if *input.Avatar == "" {
				source = "auto"
			}
			_, err = tx.Exec("UPDATE contacts SET avatar=?,avatar_source=? WHERE owner=? AND email=?", *input.Avatar, source, owner, email)
		} else if err == nil && input.Source != nil {
			_, err = tx.Exec("UPDATE contacts SET avatar_source=? WHERE owner=? AND email=?", *input.Source, owner, email)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			problem(w, 503, "Could not save contact. The address book may be full.")
			return
		}
	} else if email == "" {
		problem(w, 400, "A valid email address is required.")
		return
	}
	if r.Method == "DELETE" {
		if _, err := s.db.ExecContext(r.Context(), "UPDATE contacts SET saved=0 WHERE owner=? AND email=?", owner, email); err != nil {
			problem(w, 503, "Could not remove contact.")
		} else {
			reply(w, 200, map[string]bool{"removed": true})
		}
		return
	}
	if r.Method != "GET" && r.Method != "POST" {
		problem(w, 405, "Method not supported.")
		return
	}
	var name, avatar, source string
	var saved bool
	err := s.db.QueryRowContext(r.Context(), "SELECT name,avatar,avatar_source,saved FROM contacts WHERE owner=? AND email=?", owner, email).Scan(&name, &avatar, &source, &saved)
	if errors.Is(err, sql.ErrNoRows) {
		source = "auto"
	} else if err != nil {
		problem(w, 503, "Could not load contact.")
		return
	}
	status := 200
	if r.Method == "POST" {
		status = 201
	}
	reply(w, status, map[string]any{"contact": map[string]any{"displayName": name, "avatarSourcePreference": source, "isSaved": saved, "hasCustomAvatar": avatar != "", "outgoingCount": nil, "lastOutgoingAt": nil}})
}

func (s *service) avatar(w http.ResponseWriter, r *http.Request, owner string) {
	email := normalizedEmail(r.URL.Query().Get("email"))
	var value string
	if email == "" {
		problem(w, 400, "Invalid contact email.")
		return
	}
	if err := s.db.QueryRowContext(r.Context(), "SELECT avatar FROM contacts WHERE owner=? AND email=?", owner, email).Scan(&value); err != nil || value == "" {
		problem(w, 404, "No private photo.")
		return
	}
	parts := strings.SplitN(value, ",", 2)
	data, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		problem(w, 503, "Photo is unavailable.")
		return
	}
	w.Header().Set("Content-Type", strings.TrimSuffix(strings.TrimPrefix(parts[0], "data:"), ";base64"))
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

func ensureContact(tx *sql.Tx, owner, email, name string) error {
	var count int
	if err := tx.QueryRow("SELECT count(*) FROM contacts WHERE owner=? AND email=?", owner, email).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	if err := tx.QueryRow("SELECT count(*) FROM contacts WHERE owner=?", owner).Scan(&count); err != nil {
		return err
	}
	if count >= 10000 {
		return errors.New("address book quota reached")
	}
	_, err := tx.Exec("INSERT INTO contacts(owner,email,name,saved) VALUES(?,?,?,1)", owner, email, name)
	return err
}

func (s *service) memberships(w http.ResponseWriter, r *http.Request, owner string) {
	if r.Method == "GET" {
		email := normalizedEmail(r.URL.Query().Get("email"))
		if email == "" {
			problem(w, 400, "Invalid contact email.")
			return
		}
		rows, err := s.db.QueryContext(r.Context(), "SELECT l.id,l.name,EXISTS(SELECT 1 FROM memberships m WHERE m.owner=l.owner AND m.list_id=l.id AND m.email=?) FROM lists l WHERE l.owner=? ORDER BY l.name", email, owner)
		if err != nil {
			problem(w, 503, "Could not load memberships.")
			return
		}
		defer rows.Close()
		lists := []map[string]any{}
		for rows.Next() {
			var id, name string
			var selected bool
			if err = rows.Scan(&id, &name, &selected); err != nil {
				problem(w, 503, "Could not load memberships.")
				return
			}
			lists = append(lists, map[string]any{"id": id, "name": name, "selected": selected})
		}
		if rows.Err() != nil {
			problem(w, 503, "Could not load memberships.")
			return
		}
		reply(w, 200, map[string]any{"lists": lists})
		return
	}
	if r.Method != "PUT" {
		problem(w, 405, "Method not supported.")
		return
	}
	var input struct {
		Email string   `json:"email"`
		IDs   []string `json:"listIds"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	email := normalizedEmail(input.Email)
	if email == "" || len(input.IDs) > 500 {
		problem(w, 400, "Invalid memberships.")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "Could not update memberships.")
		return
	}
	defer tx.Rollback()
	var count int
	err = tx.QueryRow("SELECT count(*) FROM contacts c WHERE owner=? AND email=? AND (saved=1 OR EXISTS(SELECT 1 FROM memberships m WHERE m.owner=c.owner AND m.email=c.email))", owner, email).Scan(&count)
	if err != nil {
		problem(w, 503, "Could not update memberships.")
		return
	}
	if count == 0 {
		problem(w, 404, "Contact not found.")
		return
	}
	ids := map[string]bool{}
	for _, id := range input.IDs {
		if len(id) != 64 {
			problem(w, 400, "Invalid list.")
			return
		}
		if ids[id] {
			continue
		}
		ids[id] = true
		if err = tx.QueryRow("SELECT count(*) FROM lists WHERE owner=? AND id=?", owner, id).Scan(&count); err != nil {
			problem(w, 503, "Could not update memberships.")
			return
		}
		if count == 0 {
			problem(w, 404, "Contact list not found.")
			return
		}
	}
	_, err = tx.Exec("DELETE FROM memberships WHERE owner=? AND email=?", owner, email)
	for id := range ids {
		if err == nil {
			_, err = tx.Exec("INSERT INTO memberships(owner,list_id,email) VALUES(?,?,?)", owner, id, email)
		}
	}
	if err == nil {
		_, err = tx.Exec("UPDATE contacts SET saved=1 WHERE owner=? AND email=?", owner, email)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(w, 503, "Could not update memberships.")
		return
	}
	reply(w, 200, map[string]bool{"success": true})
}

type importContact struct{ email, name string }

func parseImport(data string, mapping map[string]string) ([]importContact, error) {
	if len(data) > 1<<20 {
		return nil, errors.New("CSV is too large")
	}
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(data, "\ufeff")))
	headers, err := reader.Read()
	if err != nil {
		return nil, errors.New("CSV needs column headers")
	}
	indices := map[string]int{}
	for i, header := range headers {
		indices[strings.TrimSpace(header)] = i
	}
	column, ok := indices[strings.TrimSpace(mapping["email"])]
	if !ok || mapping["email"] == "" {
		return nil, errors.New("choose an existing email column")
	}
	contacts := []importContact{}
	seen := map[string]bool{}
	for rowCount := 0; ; rowCount++ {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("invalid CSV row")
		}
		if rowCount >= 5000 {
			return nil, errors.New("import at most 5000 rows at once")
		}
		email := normalizedEmail(row[column])
		if email == "" || seen[email] {
			continue
		}
		seen[email] = true
		names := []string{}
		for _, field := range []string{"firstName", "lastName"} {
			if i, ok := indices[strings.TrimSpace(mapping[field])]; ok && mapping[field] != "" {
				names = append(names, strings.TrimSpace(row[i]))
			}
		}
		name := strings.TrimSpace(strings.Join(names, " "))
		if !validName(name, 200) {
			return nil, errors.New("contact name is too long or invalid")
		}
		contacts = append(contacts, importContact{email, name})
	}
	return contacts, nil
}

func (s *service) lists(w http.ResponseWriter, r *http.Request, owner string) {
	if r.Method == "GET" {
		rows, err := s.db.QueryContext(r.Context(), "SELECT l.id,l.name,l.created_at,(SELECT count(*) FROM memberships m WHERE m.owner=l.owner AND m.list_id=l.id) FROM lists l WHERE l.owner=? ORDER BY l.created_at DESC,l.id", owner)
		if err != nil {
			problem(w, 503, "Could not load contact lists.")
			return
		}
		defer rows.Close()
		lists := []map[string]any{}
		for rows.Next() {
			var id, name, date string
			var count int
			if err = rows.Scan(&id, &name, &date, &count); err != nil {
				problem(w, 503, "Could not load lists.")
				return
			}
			lists = append(lists, map[string]any{"id": id, "name": name, "ownerEmail": owner, "createdAt": date, "_count": map[string]int{"contacts": count}})
		}
		if rows.Err() != nil {
			problem(w, 503, "Could not load lists.")
			return
		}
		reply(w, 200, map[string]any{"lists": lists})
		return
	}
	if r.Method != "POST" {
		problem(w, 405, "Method not supported.")
		return
	}
	var input struct {
		Name    string            `json:"name"`
		CSV     string            `json:"csvData"`
		Mapping map[string]string `json:"columnMapping"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || !validName(input.Name, 120) {
		problem(w, 400, "Enter a list name (1–120 characters).")
		return
	}
	contacts := []importContact{}
	if input.CSV != "" {
		var err error
		contacts, err = parseImport(input.CSV, input.Mapping)
		if err != nil {
			problem(w, 400, err.Error())
			return
		}
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "Could not create list.")
		return
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM lists WHERE owner=?", owner).Scan(&count); err != nil {
		problem(w, 503, "Could not create list.")
		return
	}
	if count >= 500 {
		problem(w, 409, "Contact list limit reached.")
		return
	}
	id := opaqueID()
	date := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.Exec("INSERT INTO lists(owner,id,name,created_at) VALUES(?,?,?,?)", owner, id, input.Name, date)
	for _, contact := range contacts {
		if err == nil {
			err = ensureContact(tx, owner, contact.email, contact.name)
		}
		if err == nil {
			_, err = tx.Exec("INSERT INTO memberships(owner,list_id,email) VALUES(?,?,?)", owner, id, contact.email)
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(w, 503, "Could not import contacts. The address book may be full.")
		return
	}
	reply(w, 201, map[string]any{"list": map[string]any{"id": id, "name": input.Name, "ownerEmail": owner, "createdAt": date, "_count": map[string]int{"contacts": len(contacts)}}, "imported": len(contacts)})
}

func (s *service) changeList(w http.ResponseWriter, r *http.Request, owner, id string) {
	if r.Method == "PATCH" {
		var input struct {
			Name string `json:"name"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" || !validName(input.Name, 120) {
			problem(w, 400, "Enter a list name (1–120 characters).")
			return
		}
		result, err := s.db.ExecContext(r.Context(), "UPDATE lists SET name=? WHERE owner=? AND id=?", input.Name, owner, id)
		if err != nil {
			problem(w, 503, "Could not rename list.")
			return
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			problem(w, 404, "Contact list not found.")
		} else {
			reply(w, 200, map[string]bool{"success": true})
		}
		return
	}
	if r.Method != "DELETE" {
		problem(w, 405, "Method not supported.")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "Could not delete list.")
		return
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM lists WHERE owner=? AND id=?", owner, id).Scan(&count); err != nil {
		problem(w, 503, "Could not delete list.")
		return
	}
	if count == 0 {
		problem(w, 404, "Contact list not found.")
		return
	}
	_, err = tx.Exec("UPDATE contacts SET saved=1 WHERE owner=? AND email IN(SELECT email FROM memberships WHERE owner=? AND list_id=?)", owner, owner, id)
	if err == nil {
		_, err = tx.Exec("DELETE FROM lists WHERE owner=? AND id=?", owner, id)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(w, 503, "Could not delete list.")
		return
	}
	reply(w, 200, map[string]bool{"success": true})
}

func (s *service) listContacts(w http.ResponseWriter, r *http.Request, owner, id string) {
	var count int
	if err := s.db.QueryRowContext(r.Context(), "SELECT count(*) FROM lists WHERE owner=? AND id=?", owner, id).Scan(&count); err != nil {
		problem(w, 503, "Could not load list.")
		return
	}
	if count == 0 {
		problem(w, 404, "Contact list not found.")
		return
	}
	rows, err := s.db.QueryContext(r.Context(), "SELECT c.email,c.name FROM contacts c JOIN memberships m ON m.owner=c.owner AND m.email=c.email WHERE m.owner=? AND m.list_id=? ORDER BY c.email", owner, id)
	if err != nil {
		problem(w, 503, "Could not load list contacts.")
		return
	}
	defer rows.Close()
	contacts := []map[string]any{}
	for rows.Next() {
		var email, name string
		if err = rows.Scan(&email, &name); err != nil {
			problem(w, 503, "Could not load list contacts.")
			return
		}
		contacts = append(contacts, map[string]any{"id": id + ":" + email, "listId": id, "email": email, "firstName": name, "lastName": "", "status": "ACTIVE", "customFields": map[string]any{}})
	}
	if rows.Err() != nil {
		problem(w, 503, "Could not load list contacts.")
		return
	}
	reply(w, 200, map[string]any{"contacts": contacts})
}
