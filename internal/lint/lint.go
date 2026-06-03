// Package lint runs at the write boundary: every page an agent or human writes
// is checked before it lands, so AI slop never reaches the source of truth. A
// built-in title check and link resolution always run; Rules add per-wiki
// requirements from config.
package lint

import (
	"fmt"
	"regexp"
	"strings"
)

type Issue struct {
	Severity string `json:"severity"` // "error" blocks the write; "warning" is advisory
	Message  string `json:"message"`
}

// Rules are the configurable checks, loaded from a wiki's config.
type Rules struct {
	RequireFrontmatter []string     `json:"require_frontmatter"`
	BannedTerms        []BannedTerm `json:"banned_terms"`
}

type BannedTerm struct {
	Term     string `json:"term"`
	Message  string `json:"message,omitempty"`
	Severity string `json:"severity,omitempty"` // "error" or "warning" (default)
}

var wikiLink = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// Check validates a page's frontmatter and body against the known page set and
// the configured rules.
func Check(frontmatter map[string]any, body string, knownSlugs map[string]bool, rules Rules) []Issue {
	var issues []Issue

	if title, _ := frontmatter["title"].(string); strings.TrimSpace(title) == "" {
		issues = append(issues, Issue{"error", "frontmatter is missing a non-empty `title`"})
	}

	for _, field := range rules.RequireFrontmatter {
		if v, ok := frontmatter[field]; !ok || isEmpty(v) {
			issues = append(issues, Issue{"error", fmt.Sprintf("frontmatter is missing required field `%s`", field)})
		}
	}

	lowerBody := strings.ToLower(body)
	for _, b := range rules.BannedTerms {
		if b.Term == "" || !strings.Contains(lowerBody, strings.ToLower(b.Term)) {
			continue
		}
		severity := "warning"
		if b.Severity == "error" {
			severity = "error"
		}
		message := b.Message
		if message == "" {
			message = fmt.Sprintf("contains banned term %q", b.Term)
		}
		issues = append(issues, Issue{severity, message})
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

func isEmpty(v any) bool {
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}
