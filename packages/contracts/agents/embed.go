// Package agentcontract embeds the JSON Schemas for agent and tool definitions.
package agentcontract

import _ "embed"

//go:embed agent-definition.schema.json
var DefinitionSchema []byte

//go:embed tool.schema.json
var ToolSchema []byte
