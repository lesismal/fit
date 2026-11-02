// Package workbench is fit's web pages: one page, the workbench, with a menu
// of every module the console serving it runs (the config's own, behind
// admin.console, and each llm and tunnel role's) and the module's pages
// beside it. The files are plain HTML, CSS and JavaScript, with nothing
// fetched from elsewhere, served from memory by app/console.
package workbench

import (
	"embed"
	"io/fs"
)

//go:embed web
var files embed.FS

// Pages returns the workbench's files: index.html and what it loads.
func Pages() fs.FS {
	sub, _ := fs.Sub(files, "web")
	return sub
}
