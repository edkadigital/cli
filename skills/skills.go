// Package skills holds the agent skills of the CLI. `edka skill install` writes
// them from the binary, and `npx skills add edkadigital/cli` reads them from this
// directory.
//
// edka teaches the commands of the CLI, and its references come from the
// command tree. edka-app teaches an agent to package an app; `make app-skill`
// copies it from github.com/edkadigital/apps, where the package format is kept.
package skills

import "embed"

// FS holds each skill in a directory of its name.
//
//go:embed edka edka-app
var FS embed.FS

// Names are the skills in FS, in the order an install writes them.
var Names = []string{"edka", "edka-app"}
