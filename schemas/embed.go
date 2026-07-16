// Package schemas exposes the canonical public JSON Schemas to the Go runtime.
package schemas

import "embed"

// FS contains every canonical contract schema shipped with Forja.
//
//go:embed *.schema.json
var FS embed.FS
