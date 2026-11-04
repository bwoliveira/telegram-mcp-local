PRAGMA user_version = 1;

CREATE TABLE accounts (
  account        TEXT PRIMARY KEY,
  self_id        INTEGER NOT NULL DEFAULT 0,
  phone          TEXT NOT NULL DEFAULT '',
  username       TEXT NOT NULL DEFAULT '',
  first_name     TEXT NOT NULL DEFAULT '',
  last_name      TEXT NOT NULL DEFAULT '',
  premium        INTEGER NOT NULL DEFAULT 0,
  auth_state     TEXT NOT NULL DEFAULT 'none'
                 CHECK (auth_state IN ('none','authorized','logged_out','revoked')),
  auth_reason    TEXT NOT NULL DEFAULT '',
  bound_at       INTEGER,
  updated_at     INTEGER NOT NULL
);

CREATE TABLE peers (
  account        TEXT NOT NULL,
  peer_type      TEXT NOT NULL CHECK (peer_type IN ('user','chat','channel')),
  peer_id        INTEGER NOT NULL,
  access_hash    INTEGER,                       -- só o motor lê
  hash_min       INTEGER NOT NULL DEFAULT 0,
  kind           TEXT NOT NULL CHECK (kind IN ('self','user','bot','group','supergroup','channel')),
  title          TEXT NOT NULL DEFAULT '',
  first_name     TEXT NOT NULL DEFAULT '',
  last_name      TEXT NOT NULL DEFAULT '',
  username       TEXT NOT NULL DEFAULT '',
  phone          TEXT NOT NULL DEFAULT '',
  search_name    TEXT NOT NULL DEFAULT '',      -- minúsculas, sem acentos
  is_contact     INTEGER NOT NULL DEFAULT 0,
  forum          INTEGER NOT NULL DEFAULT 0,
  protected      INTEGER NOT NULL DEFAULT 0,    -- noforwards
  participants   INTEGER NOT NULL DEFAULT 0,
  access         TEXT NOT NULL DEFAULT 'ok'
                 CHECK (access IN ('ok','left','kicked','banned','lost','deleted')),
  access_reason  TEXT NOT NULL DEFAULT '',
  access_changed_at INTEGER,
  migrated_to_type TEXT,
  migrated_to_id   INTEGER,
  updated_at     INTEGER NOT NULL,
  PRIMARY KEY (account, peer_type, peer_id)
) WITHOUT ROWID;
CREATE INDEX peers_search ON peers(account, search_name);

CREATE TABLE peer_usernames (
  account TEXT NOT NULL, peer_type TEXT NOT NULL, peer_id INTEGER NOT NULL,
  username TEXT NOT NULL COLLATE NOCASE,
  PRIMARY KEY (account, peer_type, peer_id, username),
  FOREIGN KEY (account, peer_type, peer_id) REFERENCES peers ON DELETE CASCADE
) WITHOUT ROWID;
CREATE INDEX peer_usernames_lookup ON peer_usernames(account, username);

CREATE TABLE dialogs (
  account TEXT NOT NULL, peer_type TEXT NOT NULL, peer_id INTEGER NOT NULL,
  folder_id          INTEGER NOT NULL DEFAULT 0,  -- 1 = Arquivados
  pinned             INTEGER NOT NULL DEFAULT 0,
  pin_order          INTEGER NOT NULL DEFAULT 0,
  top_message_id     INTEGER NOT NULL DEFAULT 0,
  read_inbox_max_id  INTEGER NOT NULL DEFAULT 0,
  read_outbox_max_id INTEGER NOT NULL DEFAULT 0,
  unread_count       INTEGER NOT NULL DEFAULT 0,
  unread_mentions    INTEGER NOT NULL DEFAULT 0,
  unread_reactions   INTEGER NOT NULL DEFAULT 0,
  unread_mark        INTEGER NOT NULL DEFAULT 0,
  mute_until         INTEGER,
  ttl_period         INTEGER NOT NULL DEFAULT 0,
  last_message_date  INTEGER,
  refreshed_at       INTEGER NOT NULL,
  PRIMARY KEY (account, peer_type, peer_id),
  FOREIGN KEY (account, peer_type, peer_id) REFERENCES peers ON DELETE CASCADE
) WITHOUT ROWID;
CREATE INDEX dialogs_recent ON dialogs(account, last_message_date DESC);

CREATE TABLE messages (
  row_id         INTEGER PRIMARY KEY,           -- interno; rowid do FTS
  account        TEXT NOT NULL,
  peer_type      TEXT NOT NULL,
  peer_id        INTEGER NOT NULL,
  msg_id         INTEGER NOT NULL,
  date           INTEGER NOT NULL,
  edit_date      INTEGER NOT NULL DEFAULT 0,
  out            INTEGER NOT NULL DEFAULT 0,
  sender_type    TEXT,
  sender_id      INTEGER,
  sender_name    TEXT NOT NULL DEFAULT '',
  post_author    TEXT NOT NULL DEFAULT '',
  text           TEXT NOT NULL DEFAULT '',      -- texto ou legenda
  entities_json  TEXT,                          -- []engine.Entity
  media_type     TEXT,
  media_json     TEXT,                          -- engine.Media sem Remote
  media_remote   BLOB,                          -- localização opaca; só o motor lê
  media_ttl      INTEGER NOT NULL DEFAULT 0,    -- >0: visualização única, nunca baixada
  location_json  TEXT,
  poll_json      TEXT,
  reply_to_msg_id  INTEGER,
  reply_to_peer_type TEXT,
  reply_to_peer_id   INTEGER,
  reply_top_id     INTEGER,
  forward_json   TEXT,
  topic_id       INTEGER NOT NULL DEFAULT 0,
  grouped_id     INTEGER NOT NULL DEFAULT 0,
  service_type   TEXT,
  service_json   TEXT,
  mentioned      INTEGER NOT NULL DEFAULT 0,
  media_unread   INTEGER NOT NULL DEFAULT 0,
  silent         INTEGER NOT NULL DEFAULT 0,
  post           INTEGER NOT NULL DEFAULT 0,
  pinned         INTEGER NOT NULL DEFAULT 0,
  protected      INTEGER NOT NULL DEFAULT 0,
  views          INTEGER NOT NULL DEFAULT 0,
  forwards       INTEGER NOT NULL DEFAULT 0,
  ttl_period     INTEGER NOT NULL DEFAULT 0,
  expires_at     INTEGER,                       -- date + ttl_period
  partial        INTEGER NOT NULL DEFAULT 0,    -- cópia incompleta (ex.: resultado de busca)
  version        INTEGER NOT NULL DEFAULT 1,
  source         TEXT NOT NULL,
  first_seen_at  INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  UNIQUE (account, peer_type, peer_id, msg_id),
  FOREIGN KEY (account, peer_type, peer_id) REFERENCES peers
);
CREATE INDEX messages_by_date   ON messages(account, peer_type, peer_id, date);
CREATE INDEX messages_global    ON messages(account, date);
CREATE INDEX messages_out       ON messages(account, peer_type, peer_id, out, date);
CREATE INDEX messages_sender    ON messages(account, sender_type, sender_id, date);
CREATE INDEX messages_media     ON messages(account, media_type) WHERE media_type IS NOT NULL;
CREATE INDEX messages_expiry    ON messages(expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX messages_nonchan   ON messages(account, msg_id) WHERE peer_type <> 'channel';

CREATE TABLE message_tombstones (
  account TEXT NOT NULL, peer_type TEXT NOT NULL, peer_id INTEGER NOT NULL,
  msg_id INTEGER NOT NULL,
  reason     TEXT NOT NULL CHECK (reason IN ('deleted','revoked','ttl','access_purge','user_purge')),
  deleted_at INTEGER NOT NULL,
  PRIMARY KEY (account, peer_type, peer_id, msg_id)
) WITHOUT ROWID;                                -- só ids, nunca conteúdo

CREATE TABLE pending_deletes (                  -- §4: ids sem peer, ainda não vistos
  account TEXT NOT NULL, msg_id INTEGER NOT NULL, received_at INTEGER NOT NULL,
  PRIMARY KEY (account, msg_id)
) WITHOUT ROWID;

CREATE TABLE reactions (
  row_id      INTEGER PRIMARY KEY REFERENCES messages(row_id) ON DELETE CASCADE,
  counts_json TEXT NOT NULL,
  recent_json TEXT,
  counts_hash TEXT NOT NULL,
  updated_at  INTEGER NOT NULL
);

CREATE TABLE mentions (
  row_id       INTEGER PRIMARY KEY REFERENCES messages(row_id) ON DELETE CASCADE,
  account TEXT NOT NULL, peer_type TEXT NOT NULL, peer_id INTEGER NOT NULL,
  msg_id       INTEGER NOT NULL,
  topic_id     INTEGER NOT NULL DEFAULT 0,
  date         INTEGER NOT NULL,
  by_name      INTEGER NOT NULL DEFAULT 0,
  by_username  INTEGER NOT NULL DEFAULT 0,
  by_reply     INTEGER NOT NULL DEFAULT 0,
  by_flag      INTEGER NOT NULL DEFAULT 0,
  answered_at  INTEGER,
  answered_by  INTEGER,
  server_unread INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX mentions_by_date ON mentions(account, date DESC);
CREATE INDEX mentions_open    ON mentions(account, peer_type, peer_id, date) WHERE answered_at IS NULL;

CREATE TABLE groups_full (
  account TEXT NOT NULL, peer_type TEXT NOT NULL, peer_id INTEGER NOT NULL,
  about TEXT NOT NULL DEFAULT '', owner_id INTEGER, created_at INTEGER,
  linked_type TEXT, linked_id INTEGER,
  size INTEGER NOT NULL DEFAULT 0, partial INTEGER NOT NULL DEFAULT 0,
  am_admin INTEGER NOT NULL DEFAULT 0, snapshot_at INTEGER NOT NULL,
  PRIMARY KEY (account, peer_type, peer_id),
  FOREIGN KEY (account, peer_type, peer_id) REFERENCES peers ON DELETE CASCADE
) WITHOUT ROWID;

CREATE TABLE participants (
  account TEXT NOT NULL, group_type TEXT NOT NULL, group_id INTEGER NOT NULL,
  user_type TEXT NOT NULL, user_id INTEGER NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('creator','admin','member','restricted','banned','left')),
  joined_at INTEGER, updated_at INTEGER NOT NULL,
  PRIMARY KEY (account, group_type, group_id, user_type, user_id)
) WITHOUT ROWID;

-- Estado de updates (gotd updates.State), global e por canal.
CREATE TABLE updates_state (
  account     TEXT PRIMARY KEY,
  pts INTEGER NOT NULL, qts INTEGER NOT NULL, date INTEGER NOT NULL, seq INTEGER NOT NULL,
  held        INTEGER NOT NULL DEFAULT 0,       -- I4
  held_reason TEXT NOT NULL DEFAULT '',
  last_update_at INTEGER,
  updated_at  INTEGER NOT NULL
);
CREATE TABLE channel_state (
  account TEXT NOT NULL, channel_id INTEGER NOT NULL,
  pts INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  PRIMARY KEY (account, channel_id)
) WITHOUT ROWID;                                -- access_hash fica em peers

CREATE TABLE backfill_cursors (
  account TEXT NOT NULL, peer_type TEXT NOT NULL, peer_id INTEGER NOT NULL,
  newest_id   INTEGER NOT NULL DEFAULT 0,
  oldest_id   INTEGER NOT NULL DEFAULT 0,
  oldest_date INTEGER,
  complete    INTEGER NOT NULL DEFAULT 0,
  state       TEXT NOT NULL DEFAULT 'idle'
              CHECK (state IN ('idle','running','flood_wait','no_access','error')),
  flood_until INTEGER,
  last_error  TEXT NOT NULL DEFAULT '',
  fetched     INTEGER NOT NULL DEFAULT 0,
  updated_at  INTEGER NOT NULL,
  PRIMARY KEY (account, peer_type, peer_id)
) WITHOUT ROWID;

CREATE TABLE gaps (
  id INTEGER PRIMARY KEY,
  account TEXT NOT NULL, peer_type TEXT, peer_id INTEGER,   -- NULL = conta inteira
  reason TEXT NOT NULL CHECK (reason IN ('channel_too_long','difference_too_long','state_lost','access_lost')),
  from_id INTEGER, to_id INTEGER,
  detected_at INTEGER NOT NULL, filled_at INTEGER
);
CREATE INDEX gaps_open ON gaps(account, peer_type, peer_id) WHERE filled_at IS NULL;

-- Mídia baixada e transcrições: somem com a mensagem.
CREATE TABLE media_files (
  row_id INTEGER NOT NULL REFERENCES messages(row_id) ON DELETE CASCADE,
  variant TEXT NOT NULL CHECK (variant IN ('full','thumb')),
  path TEXT NOT NULL UNIQUE, bytes INTEGER NOT NULL, sha256 TEXT NOT NULL,
  downloaded_at INTEGER NOT NULL,
  PRIMARY KEY (row_id, variant)
);
CREATE TABLE profile_photos (
  account TEXT NOT NULL, peer_type TEXT NOT NULL, peer_id INTEGER NOT NULL,
  variant TEXT NOT NULL, path TEXT NOT NULL UNIQUE, bytes INTEGER NOT NULL,
  downloaded_at INTEGER NOT NULL,
  PRIMARY KEY (account, peer_type, peer_id, variant)
);
CREATE TABLE files_to_unlink (                  -- o sistema de arquivos não é transacional
  path TEXT PRIMARY KEY, queued_at INTEGER NOT NULL, reason TEXT NOT NULL
);
CREATE TRIGGER media_files_ad AFTER DELETE ON media_files BEGIN
  INSERT OR IGNORE INTO files_to_unlink(path, queued_at, reason) VALUES (old.path, unixepoch(), 'message_gone');
END;

CREATE TABLE transcripts (
  row_id INTEGER PRIMARY KEY REFERENCES messages(row_id) ON DELETE CASCADE,
  text TEXT NOT NULL, raw_text TEXT, language TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '', source TEXT NOT NULL CHECK (source IN ('local','manual','telegram')),
  created_at INTEGER NOT NULL
);

CREATE TABLE exports (
  id INTEGER PRIMARY KEY, account TEXT NOT NULL,
  path TEXT NOT NULL UNIQUE, filter_json TEXT NOT NULL,
  count INTEGER NOT NULL, bytes INTEGER NOT NULL, created_at INTEGER NOT NULL
);
CREATE TRIGGER exports_ad AFTER DELETE ON exports BEGIN
  INSERT OR IGNORE INTO files_to_unlink(path, queued_at, reason) VALUES (old.path, unixepoch(), 'export_gone');
END;

-- FTS5 contentless (§8). rowid = messages.row_id.
CREATE VIRTUAL TABLE messages_fts USING fts5(
  text, transcript, sender,
  content='', contentless_delete=1,
  tokenize='unicode61 remove_diacritics 2', prefix='2 3'
);
INSERT INTO messages_fts(messages_fts, rank) VALUES ('secure-delete', 1);

CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, text, transcript, sender) VALUES (new.row_id, new.text, '', new.sender_name);
END;
CREATE TRIGGER messages_au AFTER UPDATE OF text, sender_name ON messages BEGIN
  DELETE FROM messages_fts WHERE rowid = old.row_id;
  INSERT INTO messages_fts(rowid, text, transcript, sender)
    VALUES (new.row_id, new.text, coalesce((SELECT text FROM transcripts WHERE row_id = new.row_id), ''), new.sender_name);
END;
CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
  DELETE FROM messages_fts WHERE rowid = old.row_id;
END;
CREATE TRIGGER transcripts_aiu AFTER INSERT ON transcripts BEGIN
  DELETE FROM messages_fts WHERE rowid = new.row_id;
  INSERT INTO messages_fts(rowid, text, transcript, sender)
    SELECT row_id, text, new.text, sender_name FROM messages WHERE row_id = new.row_id;
END;
CREATE TRIGGER transcripts_ad AFTER DELETE ON transcripts BEGIN
  DELETE FROM messages_fts WHERE rowid = old.row_id;
  INSERT INTO messages_fts(rowid, text, transcript, sender)
    SELECT row_id, text, '', sender_name FROM messages WHERE row_id = old.row_id;
END;
