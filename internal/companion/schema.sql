-- Dedicated companion storage. Never run this against the mail server database.
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY, owner TEXT NOT NULL, expires INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_expiry ON sessions(expires);
CREATE TABLE IF NOT EXISTS contacts (
  owner TEXT NOT NULL, email TEXT NOT NULL, name TEXT NOT NULL DEFAULT '',
  saved INTEGER NOT NULL DEFAULT 1, avatar TEXT NOT NULL DEFAULT '',
  avatar_source TEXT NOT NULL DEFAULT 'auto',
  PRIMARY KEY(owner,email)
);
CREATE TABLE IF NOT EXISTS lists (
  owner TEXT NOT NULL, id TEXT NOT NULL, name TEXT NOT NULL, created_at TEXT NOT NULL,
  PRIMARY KEY(owner,id)
);
CREATE TABLE IF NOT EXISTS memberships (
  owner TEXT NOT NULL, list_id TEXT NOT NULL, email TEXT NOT NULL,
  PRIMARY KEY(owner,list_id,email),
  FOREIGN KEY(owner,list_id) REFERENCES lists(owner,id) ON DELETE CASCADE,
  FOREIGN KEY(owner,email) REFERENCES contacts(owner,email) ON DELETE CASCADE
);
INSERT OR IGNORE INTO schema_version(version) VALUES(1);
CREATE TABLE IF NOT EXISTS templates (
  owner TEXT NOT NULL, id TEXT NOT NULL, name TEXT NOT NULL, subject TEXT NOT NULL,
  html_body TEXT NOT NULL, created_at TEXT NOT NULL,
  PRIMARY KEY(owner,id)
);
INSERT OR IGNORE INTO schema_version(version) VALUES(2);
CREATE TABLE IF NOT EXISTS delivery_consents (owner TEXT PRIMARY KEY, consent_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS delivery_jobs (
 owner TEXT NOT NULL, id TEXT NOT NULL, campaign_id TEXT NOT NULL DEFAULT '',
 payload TEXT NOT NULL, due INTEGER NOT NULL, status TEXT NOT NULL DEFAULT 'PENDING',
 error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, sent_at TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(owner,id)
);
CREATE INDEX IF NOT EXISTS delivery_due ON delivery_jobs(status,due);
CREATE TABLE IF NOT EXISTS campaigns (
 owner TEXT NOT NULL, id TEXT NOT NULL, name TEXT NOT NULL, template_id TEXT NOT NULL,
 list_id TEXT NOT NULL, template_name TEXT NOT NULL, subject TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'DRAFT', created_at TEXT NOT NULL, scheduled_at TEXT,
 PRIMARY KEY(owner,id)
);
INSERT OR IGNORE INTO schema_version(version) VALUES(4);
CREATE TABLE IF NOT EXISTS labels (
 owner TEXT NOT NULL,id TEXT NOT NULL,name TEXT NOT NULL,color TEXT NOT NULL,
 PRIMARY KEY(owner,id),UNIQUE(owner,name)
);
CREATE TABLE IF NOT EXISTS push_registrations (
 owner TEXT NOT NULL, id TEXT NOT NULL, token TEXT NOT NULL, expires INTEGER NOT NULL,
 PRIMARY KEY(owner,id)
);
CREATE TABLE IF NOT EXISTS mail_events (id TEXT PRIMARY KEY, digest TEXT NOT NULL, expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS push_jobs (
 event_id TEXT NOT NULL, owner TEXT NOT NULL, registration_id TEXT NOT NULL,
 validity INTEGER NOT NULL, last_uid INTEGER NOT NULL, due INTEGER NOT NULL,
 expires INTEGER NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(event_id,owner,registration_id),
 FOREIGN KEY(owner,registration_id) REFERENCES push_registrations(owner,id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS push_jobs_due ON push_jobs(due);
INSERT OR IGNORE INTO schema_version(version) VALUES(6);
CREATE TABLE IF NOT EXISTS identity_profiles (
 owner TEXT PRIMARY KEY, payload TEXT NOT NULL, updated_at TEXT NOT NULL
);
INSERT OR IGNORE INTO schema_version(version) VALUES(7);
