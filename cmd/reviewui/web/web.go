// Package web is the demo shell: the page the in-browser review UI
// (cmd/reviewui/main_js.go) runs under on a static host. The shell is the
// browser's side of the server it hosts; internal/tools/demosite builds the
// site around it.
package web

import "embed"

// Files are the shell's page and script.
//
//go:embed index.html shell.js
var Files embed.FS
