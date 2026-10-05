-- A script of several statements used to be refused whatever was in it, which
-- also refused every maintenance script someone writes by hand. A script is
-- now read as one statement carrying the riskiest thing in it, so a DROP or a
-- DELETE without a WHERE buried in one is still refused by the rules that
-- already existed. The new policy keeps the old behaviour available: an
-- installation several people share keeps failing closed, and a personal
-- workspace runs the script.
CREATE TABLE IF NOT EXISTS installation_mode (id INTEGER PRIMARY KEY CHECK(id=1), mode TEXT NOT NULL);

INSERT INTO policies(id, org_id, name, rule_type, effect, enabled, config)
SELECT lower(hex(randomblob(16))),
       o.id,
       'deny_multi_statement',
       'deny_multi_statement',
       'deny',
       CASE WHEN (SELECT mode FROM installation_mode WHERE id = 1) = 'personal' THEN 0 ELSE 1 END,
       NULL
FROM organizations o
-- SQLite needs a WHERE here to tell the SELECT's own clauses from the
-- upsert's, so the always-true one is required rather than decorative.
WHERE true
ON CONFLICT(org_id, rule_type) DO NOTHING;
