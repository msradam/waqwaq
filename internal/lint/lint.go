// Package lint runs at the write boundary: every page an agent or human writes
// is checked before it lands, so AI slop never reaches the source of truth.
package lint

import (
	"regexp"
	"strings"
)

type Issue struct {
	Severity string `json:"severity"` // "error" blocks the write; "warning" is advisory
	Message  string `json:"message"`
}

var wikiLink = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// Check validates a page's frontmatter and body against the known page set.
func Check(frontmatter map[string]any, body string, knownSlugs map[string]bool) []Issue {
	var issues []Issue

	if title, _ := frontmatter["title"].(string); strings.TrimSpace(title) == "" {
		issues = append(issues, Issue{"error", "frontmatter is missing a non-empty `title`"})
	}

	seen := map[string]bool{}
	for _, m := range wikiLink.FindAllStringSubmatch(body, -1) {
		target := m[1]
		if i := strings.IndexAny(target, "|#"); i >= 0 {
			target = target[:i]
		}
		target = strings.TrimSpace(target)
		if target == "" || seen[target] {
			continue
		}
		seen[target] = true
		if !knownSlugs[target] {
			issues = append(issues, Issue{"warning", "wikilink [[" + target + "]] does not resolve to a known page"})
		}
	}
	return issues
}

func HasErrors(issues []Issue) bool {
	for _, i := range issues {
		if i.Severity == "error" {
			return true
		}
	}
	return false
}
