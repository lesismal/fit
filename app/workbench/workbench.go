// Package workbench is fit's web pages: one page, the workbench, with a menu
// of every module the console serving it runs (the config's own, behind
// admin.console, and each llm and tunnel role's) and the module's pages
// beside it. The files are plain HTML, CSS and JavaScript, with nothing
// fetched from elsewhere, served from memory by app/console.
package workbench

import (
	"embed"
	"encoding/json"
	"io/fs"
	"strings"
	"sync"
)

//go:embed web
var files embed.FS

// Pages returns the workbench's files: index.html and what it loads.
func Pages() fs.FS {
	sub, _ := fs.Sub(files, "web")
	return sub
}

// zh is the Chinese of the schema's descriptions, read once.
var zh = sync.OnceValue(func() map[string]string {
	src, err := fs.ReadFile(files, "web/gateway/schema-zh.js")
	if err != nil {
		return nil
	}
	s := string(src)
	const set = "window.FitSchemaZh = "
	start, end := strings.Index(s, set), strings.LastIndex(s, ";")
	if start < 0 || end < start {
		return nil
	}
	var m map[string]string
	if json.Unmarshal([]byte(s[start+len(set):end]), &m) != nil {
		return nil
	}
	return m
})

// SchemaZh returns the Chinese of the config schema's descriptions, by
// their English text, as the pages show them: gateway/schema-zh.js's. The
// assistant's schema_describe answers in Chinese with it.
func SchemaZh() map[string]string { return zh() }
