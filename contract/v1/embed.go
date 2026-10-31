// Package binding embeds the S1 binding contract without filesystem lookup.
package binding

import _ "embed"

// Schema is the ledger-http-draft-1 schema.
//
//go:embed schema.json
var Schema []byte
