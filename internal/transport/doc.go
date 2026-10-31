// Package transport implements the ledger-http-draft-1 HTTP binding for Actor
// credentials. It derives trusted host context from a fixed digest roster and
// forwards commands, actions and queries to the application transaction boundary.
// Server listeners, TLS termination and configuration belong to the daemon.
package transport
