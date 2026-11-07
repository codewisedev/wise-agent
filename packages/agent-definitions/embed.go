// Package agentdefs embeds agent definitions and the tool catalog so the
// control plane can register them at startup.
package agentdefs

import "embed"

//go:embed agents/*.yaml tools.yaml
var FS embed.FS
