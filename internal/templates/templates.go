package templates

import "embed"

// FS embeds all HTML templates.
//
//go:embed *.html
var FS embed.FS
