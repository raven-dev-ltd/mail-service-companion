package companion

import (
	"encoding/json"
	"testing"
)

func TestIdentityProfileIsolationAndPolicy(t *testing.T) {
	s := testService(t)
	first := login(t, s, "first@example.test")
	second := login(t, s, "second@example.test")
	request(t, s, "GET", "/api/profile", "", "", 401)
	body := `{"source":"companion","profile":{"defaultFromName":"Support","replyToAddress":"help@example.test","emailSignature":"<b>Support</b>","signatureMode":"template","signatureEnabled":true,"signaturePlacement":"below","signatureTemplate":{"name":"Support team","websiteUrl":"https://example.test"}}}`
	result := request(t, s, "PUT", "/api/profile", first, body, 200)
	if result["source"] != "companion" {
		t.Fatal(result)
	}
	request(t, s, "PUT", "/api/profile", first, body, 200) // absolute, idempotent replacement
	got := request(t, s, "GET", "/api/profile", first, "", 200)["profile"].(map[string]any)
	if got["defaultFromName"] != "Support" || got["replyToAddress"] != "help@example.test" {
		t.Fatal(got)
	}
	if len(request(t, s, "GET", "/api/profile", second, "", 200)["profile"].(map[string]any)) != 0 {
		t.Fatal("cross-account profile leak")
	}
	for _, invalid := range []string{`{"source":"provider","profile":{"defaultFromName":"Name"}}`, `{"source":"companion","profile":{"password":"secret"}}`, `{"source":"companion","profile":{"replyToAddress":"bad\r\nBcc: other@example.test"}}`, `{"source":"companion","profile":{"signatureTemplate":{"logoUrl":"javascript:alert(1)"}}}`} {
		request(t, s, "PUT", "/api/profile", first, invalid, 400)
	}
	s.config.DisabledFeatures = []string{"identityProfile"}
	request(t, s, "GET", "/api/profile", first, "", 501)
}
func TestProfileSchemaSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	db, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := json.Marshal(map[string]any{"defaultFromName": "Saved"})
	if _, err = db.Exec("INSERT INTO identity_profiles VALUES(?,?,?)", "first@example.test", string(profile), "now"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err = db.QueryRow("SELECT payload FROM identity_profiles WHERE owner=?", "first@example.test").Scan(&value); err != nil || value != string(profile) {
		t.Fatal(value, err)
	}
}
