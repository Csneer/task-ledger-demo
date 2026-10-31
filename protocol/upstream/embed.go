// Package upstream embeds the pinned task-core schema without filesystem lookup.
package upstream

import _ "embed"

// Schema is the unmodified, pinned upstream schema.
//
//go:embed schema.json
var Schema []byte
