// Package prompts embeds versioned agent system prompts (<agent>/system.md).
package prompts

import "embed"

//go:embed */*.md
var FS embed.FS
