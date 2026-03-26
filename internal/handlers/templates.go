package handlers

import (
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PageTemplates maps page names to their cloned template set.
type PageTemplates map[string]*template.Template

// LoadTemplates parses all templates using the clone-per-page pattern.
func LoadTemplates(dir string) (PageTemplates, error) {
	funcMap := template.FuncMap{
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		"formatTime": func(t time.Time) string {
			return t.Format("15:04:05")
		},
		"formatDateTime": func(t time.Time) string {
			return t.Format("2006-01-02 15:04:05")
		},
		"formatDate": func(t time.Time) string {
			return t.Format("2006-01-02")
		},
		"humanSize": func(bytes int64) string {
			switch {
			case bytes >= 1<<20:
				return fmt.Sprintf("%.1f MB", float64(bytes)/float64(1<<20))
			case bytes >= 1<<10:
				return fmt.Sprintf("%.1f KB", float64(bytes)/float64(1<<10))
			default:
				return fmt.Sprintf("%d B", bytes)
			}
		},
		"statusClass": func(status int64) string {
			switch {
			case status >= 500:
				return "status-5xx"
			case status >= 400:
				return "status-4xx"
			case status >= 300:
				return "status-3xx"
			default:
				return "status-2xx"
			}
		},
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "..."
		},
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		"dict": func(pairs ...any) map[string]any {
			m := make(map[string]any, len(pairs)/2)
			for i := 0; i < len(pairs)-1; i += 2 {
				key, _ := pairs[i].(string)
				m[key] = pairs[i+1]
			}
			return m
		},
		"statusText": func(status int64) string {
			return http.StatusText(int(status))
		},
		"multiply": func(a, b int) int { return a * b },
		"divFloat": func(a, b int64) float64 {
			if b == 0 {
				return 0
			}
			return float64(a) / float64(b) * 100
		},
	}

	// 1. Parse layouts + partials into a shared base.
	base := template.New("").Funcs(funcMap)

	layoutFiles, _ := filepath.Glob(filepath.Join(dir, "layouts", "*.html"))
	if len(layoutFiles) > 0 {
		base = template.Must(base.ParseFiles(layoutFiles...))
	}

	partialFiles, _ := filepath.Glob(filepath.Join(dir, "partials", "*.html"))
	if len(partialFiles) > 0 {
		base = template.Must(base.ParseFiles(partialFiles...))
	}

	// 2. Walk pages directory — clone base for each page file.
	pages := make(PageTemplates)
	pagesDir := filepath.Join(dir, "pages")

	filepath.WalkDir(pagesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".html" {
			return err
		}

		// Skip fragment files (prefixed with _) as standalone pages.
		if strings.HasPrefix(d.Name(), "_") {
			return nil
		}

		rel, _ := filepath.Rel(pagesDir, path)
		rel = filepath.ToSlash(rel)
		name := rel[:len(rel)-len(".html")]

		clone := template.Must(base.Clone())
		pages[name] = template.Must(clone.ParseFiles(path))

		// Also parse sibling fragments into the same clone.
		dir := filepath.Dir(path)
		frags, _ := filepath.Glob(filepath.Join(dir, "_*.html"))
		for _, f := range frags {
			pages[name] = template.Must(pages[name].ParseFiles(f))
		}

		return nil
	})

	return pages, nil
}
