package handlers

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/exploded/monitor/internal/reputation"
)

// melbourne is the IANA timezone used for all displayed timestamps.
var melbourne *time.Location

func init() {
	var err error
	melbourne, err = time.LoadLocation("Australia/Melbourne")
	if err != nil {
		log.Fatalf("failed to load Australia/Melbourne timezone: %v", err)
	}
}

// PageTemplates maps page names to their cloned template set.
type PageTemplates map[string]*template.Template

// LoadTemplates parses all templates using the clone-per-page pattern.
func LoadTemplates(dir string) (PageTemplates, error) {
	funcMap := template.FuncMap{
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		"formatTime": func(t time.Time) string {
			return t.In(melbourne).Format("15:04:05")
		},
		"formatDateTime": func(t time.Time) string {
			return t.In(melbourne).Format("2006-01-02 15:04:05")
		},
		"formatDate": func(t time.Time) string {
			return t.In(melbourne).Format("2006-01-02")
		},
		"localHour": func(utcHour string) string {
			t, err := time.Parse("2006-01-02 15:00", utcHour)
			if err != nil {
				return utcHour
			}
			return t.In(melbourne).Format("2006-01-02 15:00")
		},
		"localMinute": func(utcMin string) string {
			t, err := time.Parse("2006-01-02 15:04", utcMin)
			if err != nil {
				return utcMin
			}
			return t.In(melbourne).Format("2006-01-02 15:04")
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
		"shortHour": func(s string) string {
			// "2026-03-30 14:00" → "14:00"
			if i := strings.LastIndex(s, " "); i >= 0 && i+1 < len(s) {
				return s[i+1:]
			}
			return s
		},
		"mod": func(a, b int) int {
			if b == 0 {
				return 0
			}
			return a % b
		},
		"chartLabel": func(s string, step int) string {
			// For short ranges (step 1), just show time "14:00"
			if step <= 1 {
				if i := strings.LastIndex(s, " "); i >= 0 && i+1 < len(s) {
					return s[i+1:]
				}
				return s
			}
			// For multi-day ranges, parse and show date + time
			t, err := time.Parse("2006-01-02 15:04", s)
			if err != nil {
				return s
			}
			if step >= 24 {
				return t.Format("Jan 2")
			}
			return t.Format("Jan 2 15h")
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
		"toInt64": func(f float64) int64 { return int64(f) },
		"threatBadge": func(score int) template.HTML {
			cls := reputation.BadgeClass(score)
			if cls == "" {
				return ""
			}
			return template.HTML(`<span class="threat-badge ` + cls + `">` + strconv.Itoa(score) + `</span>`)
		},
		"refererLabel": func(ref string) string {
			r := strings.ToLower(ref)
			switch {
			case strings.Contains(r, "google."):
				return "Google"
			case strings.Contains(r, "bing."):
				return "Bing"
			case strings.Contains(r, "duckduckgo."):
				return "DuckDuckGo"
			case strings.Contains(r, "yahoo."):
				return "Yahoo"
			case strings.Contains(r, "facebook.") || strings.Contains(r, "fb."):
				return "Facebook"
			case strings.Contains(r, "twitter.") || strings.Contains(r, "t.co"):
				return "Twitter/X"
			case strings.Contains(r, "reddit."):
				return "Reddit"
			default:
				return ref
			}
		},
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
