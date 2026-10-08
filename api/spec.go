// Package apispec embeds the OpenAPI contract so the service can serve it.
package apispec

import _ "embed"

//go:embed openapi.yaml
var Spec []byte
