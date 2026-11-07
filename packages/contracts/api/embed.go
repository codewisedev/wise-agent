// Package apicontract embeds the Wise OpenAPI document.
package apicontract

import _ "embed"

//go:embed openapi.yaml
var Spec []byte
