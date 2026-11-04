// Package api holds the public contract. api/openapi.json is the single source the client generates its types from.
package api

import _ "embed"

//go:embed openapi.json
var OpenAPI []byte
