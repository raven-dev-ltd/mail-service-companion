package companion

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTemplatesCRUDAndIsolation(t *testing.T) {
	s := testService(t)
	a := login(t, s, "a@example.test")
	b := login(t, s, "b@example.test")
	request(t, s, "GET", "/api/templates", "", "", 401)
	result := request(t, s, "POST", "/api/templates", a, `{"name":"Welcome","subject":"Hello","htmlBody":"<p>Welcome</p>"}`, 201)
	id := result["template"].(map[string]any)["id"].(string)
	if len(request(t, s, "GET", "/api/templates", b, "", 200)["templates"].([]any)) != 0 {
		t.Fatal("templates leaked across users")
	}
	request(t, s, "PUT", "/api/templates/"+id, b, `{"name":"Other","subject":"Hi","htmlBody":"text"}`, 404)
	request(t, s, "DELETE", "/api/templates/"+id, b, "", 404)
	updated := request(t, s, "PUT", "/api/templates/"+id, a, `{"name":"Updated","subject":"Hi","htmlBody":"<b>Updated</b>"}`, 200)
	if updated["template"].(map[string]any)["name"] != "Updated" {
		t.Fatal(updated)
	}
	if len(request(t, s, "GET", "/api/templates", a, "", 200)["templates"].([]any)) != 1 {
		t.Fatal("template not saved")
	}
	request(t, s, "DELETE", "/api/templates/"+id, a, "", 200)
	request(t, s, "DELETE", "/api/templates/"+id, a, "", 404)
}

func TestTemplateValidationAndCapabilities(t *testing.T) {
	s := testService(t)
	token := login(t, s, "a@example.test")
	for _, body := range []string{`{"name":"","subject":"Hi","htmlBody":"body"}`, `{"name":"Hi","subject":"bad\r\nHeader","htmlBody":"body"}`, `{"name":"Hi","subject":"Hi","htmlBody":"body","owner":"other@example.test"}`} {
		request(t, s, "POST", "/api/templates", token, body, 400)
	}
	body, _ := json.Marshal(map[string]string{"name": "Big", "subject": "Hi", "htmlBody": strings.Repeat("x", (128<<10)+1)})
	request(t, s, "POST", "/api/templates", token, string(body), 400)
	request(t, s, "PUT", "/api/templates", token, `{}`, 405)
	request(t, s, "GET", "/api/templates/not-an-id", token, "", 404)
	features := request(t, s, "GET", "/api/native/capabilities", token, "", 200)["features"].(map[string]any)
	if features["templates"] != true || features["campaigns"] != false {
		t.Fatal("unimplemented features advertised", features)
	}
}

func TestTemplateMigrationPreservesExistingContacts(t *testing.T) {
	dir := t.TempDir()
	db, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("DROP TABLE templates; DROP TABLE delivery_consents; DROP TABLE delivery_jobs; DROP TABLE campaigns; DROP TABLE labels; DELETE FROM schema_version WHERE version>1; INSERT INTO contacts(owner,email,name) VALUES('a@example.test','b@example.test','Existing')")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRow("SELECT count(*) FROM contacts").Scan(&count); err != nil || count != 1 {
		t.Fatal("contacts lost", err, count)
	}
	if err = db.QueryRow("SELECT count(*) FROM templates").Scan(&count); err != nil || count != 0 {
		t.Fatal("migration failed", err, count)
	}
}
