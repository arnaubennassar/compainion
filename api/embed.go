// Package api embeds the OpenAPI spec served at GET /openapi.yaml.
package api

import _ "embed"

//go:embed openapi.yaml
var Spec []byte
