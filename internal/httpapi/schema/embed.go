// Package schema embeds the weave HTTP API OpenAPI document so the server can
// serve it (GET /weave/openapi.yaml) and tooling can consume it directly.
package schema

import _ "embed"

// OpenAPI is the OpenAPI 3.1 document describing the weave HTTP API.
//
//go:embed openapi.yaml
var OpenAPI []byte
