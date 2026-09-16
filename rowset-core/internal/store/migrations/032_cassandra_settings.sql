ALTER TABLE connections ADD COLUMN cassandra_consistency TEXT NOT NULL DEFAULT 'QUORUM';
ALTER TABLE connections ADD COLUMN cassandra_page_size INTEGER NOT NULL DEFAULT 1000;
