package kv

import "database/sql"

// SQL is what a SQL backend gives the rest of the store: the key-value space (DB), the handles the
// event tables live beside, the migration's combined transaction, and what the command line's checks
// ask of the database itself. SQLite and PostgreSQL both satisfy it; which one a deployment runs is
// the storage.driver configuration, and nothing above this interface knows the difference.
type SQL interface {
	DB
	// Handle is the read pool, for what lives beside the key-value space (the event tables); a
	// query on it sees the committed state, not what an open Update holds.
	Handle() *sql.DB
	// WriteHandle is the write pool, whose single connection serialises the writes; a transaction
	// on it can span the key-value space and the event tables at once.
	WriteHandle() *sql.DB
	// UpdateBoth runs fn in one write transaction, giving it the key-value space and the
	// database/sql transaction under it, for what spans the two (a migration copies buckets and
	// writes the event tables' records together).
	UpdateBoth(fn func(tx Tx, raw *sql.Tx) error) error
	// Health is the backend's own check of the whole database: "ok", or the findings. SQLite runs
	// its integrity check; PostgreSQL reads every table the store keeps.
	Health() (string, error)
	// Where names the database for the command line, without any secret the DSN carries.
	Where() string
}
