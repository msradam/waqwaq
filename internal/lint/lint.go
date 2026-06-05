// Package lint checks a page before it is written: a non-empty title is required
// and wikilinks are resolved against the known pages. Rules add per-wiki
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

	switch t := frontmatter["title"].(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			issues = append(issues, Issue{"error", "frontmatter `title` is empty"})
		}
	case nil:
		if frontmatter == nil {
			issues = append(issues, Issue{"error", "page has no parseable YAML frontmatter; add a `---` block with a `title`"})
		} else {
			issues = append(issues, Issue{"error", "frontmatter is missing a `title`"})
		}
	default:
		issues = append(issues, Issue{"error", fmt.Sprintf("frontmatter `title` must be a string, got %T", t)})
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

	// Strip code before scanning wikilinks so a [[link]] shown inside a code block
	// is not flagged, matching how the link graph resolves edges.
	seen := map[string]bool{}
	unresolved := 0
	for _, m := range wikiLink.FindAllStringSubmatch(stripCode(body), -1) {
		target := m[1]
		if i := strings.IndexAny(target, "|#"); i >= 0 {
			target = target[:i]
		}
		target = strings.TrimSpace(target)
		if target == "" || seen[target] {
			continue
		}
		seen[target] = true
		if knownSlugs[target] {
			continue
		}
		unresolved++
		if unresolved <= maxLinkWarnings {
			issues = append(issues, Issue{"warning", "wikilink [[" + target + "]] does not resolve to a known page"})
		}
	}
	if unresolved > maxLinkWarnings {
		issues = append(issues, Issue{"warning", fmt.Sprintf("and %d more unresolved wikilinks", unresolved-maxLinkWarnings)})
	}
	return issues
}

const maxLinkWarnings = 20

var (
	fenceRe = regexp.MustCompile("(?s)```.*?```|~~~.*?~~~")
	icodeRe = regexp.MustCompile("`[^`\n]*`")
)

// stripCode removes fenced and inline code so their contents are not linted as
// page content. It mirrors the link graph's code handling.
func stripCode(body string) string {
	body = fenceRe.ReplaceAllString(body, "")
	return icodeRe.ReplaceAllString(body, "")
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
