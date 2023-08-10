BEGIN EXCLUSIVE TRANSACTION;

CREATE TABLE IF NOT EXISTS queries (
  query_id INTEGER PRIMARY KEY AUTOINCREMENT,
  query    TEXT NOT NULL,  -- SQL query text

  UNIQUE (query)
);

CREATE TABLE IF NOT EXISTS raw_query_log (
  author   TEXT NULL,      -- login of query author, if known
  source   TEXT NOT NULL,  -- source label, e.g., "main", "raw"

  query_id INTEGER NOT NULL
     REFERENCES queries (query_id),
  timestamp TIMESTAMP NOT NULL
     DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now'))
);

INSERT INTO queries (query) SELECT distinct query FROM query_log;

INSERT INTO raw_query_log (author, source, query_id, timestamp)
  SELECT author, source, query_id, timestamp
    FROM query_log JOIN queries USING (query)
;

DROP TABLE query_log;

CREATE VIEW IF NOT EXISTS query_log AS
  SELECT author, source, query, timestamp
    FROM raw_query_log JOIN queries
   USING (query_id)
;

COMMIT;
