package static

import "embed"

// FS embeds static assets (CSS, test sample emails).
//
//go:embed css/* samples/*
var FS embed.FS
