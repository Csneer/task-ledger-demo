// Package core implements pure candidate transitions for task-core-draft-1.
// Callers validate protocol shapes and provide trusted identity before invoking
// it, then atomically persist accepted candidates with their events and receipts.
// This package performs no I/O, authentication, persistence, or idempotent replay.
package core
