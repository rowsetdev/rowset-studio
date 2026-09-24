-- A workspace on someone's own machine no longer blocks SQL Rowset has not
-- learned to classify: it runs and the result says it was not recognised.
-- The guardrails that matter there - DROP, TRUNCATE, UPDATE and DELETE
-- without a WHERE - are untouched, and the policy stays available in My
-- policies. An installation several people share keeps failing closed.
CREATE TABLE IF NOT EXISTS installation_mode (id INTEGER PRIMARY KEY CHECK(id=1), mode TEXT NOT NULL);

UPDATE policies SET enabled = 0
WHERE rule_type = 'deny_unclassified'
  AND (SELECT mode FROM installation_mode WHERE id = 1) = 'personal';
