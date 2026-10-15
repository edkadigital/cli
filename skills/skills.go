// Package skills holds the agent skills of the CLI. `edka skill install` writes
// them from the binary, and `npx skills add edkadigital/cli` reads them from this
// directory.
package skills

import "embed"

// Edka is the edka skill, under the directory edka.
//
//go:embed edka
var Edka embed.FS
