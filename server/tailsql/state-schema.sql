-- Local state schema.

-- Unique queries by spelling.
CREATE TABLE IF NOT EXISTS queries (
  query_id INTEGER PRIMARY KEY AUTOINCREMENT,
  query    TEXT NOT NULL,  -- SQL query text

  UNIQUE (query)
);

-- A log of all successful queries completed by the service, per user.
CREATE TABLE IF NOT EXISTS raw_query_log (
  author   TEXT NULL,      -- login of query author, if known
  source   TEXT NOT NULL,  -- source label, e.g., "main", "raw"

  query_id INTEGER NOT NULL
     REFERENCES queries (query_id),
  timestamp TIMESTAMP NOT NULL
     DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now')),
  elapsed INTEGER NULL     -- microseconds
);

-- A joined view of the query log.
CREATE VIEW IF NOT EXISTS query_log AS
  SELECT author, source, query, timestamp, elapsed
    FROM raw_query_log JOIN queries
   USING (query_id)
;
