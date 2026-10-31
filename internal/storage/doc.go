// Package storage provides file-backed SQLite transactions for opaque task facts,
// receipts, application records, and an append-only event log. It does not perform
// protocol validation or authorization; callers must do both inside Update before
// committing facts. Tx values are callback-scoped and must not be used concurrently.
package storage
