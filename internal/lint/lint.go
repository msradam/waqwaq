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

// wikiLink captures an optional leading ! so embeds (![[...]]) can be skipped,
// matching the link graph, which treats embeds as content rather than edges.
var wikiLink = regexp.MustCompile(`(!?)\[\[([^\]]+)\]\]`)

// Check validates a page's frontmatter and body against the configured rules.
// resolves reports whether a wikilink target is fine, using the store's tolerant
// resolution so lint agrees with the link graph (case, spaces, basename); pass
// nil to treat every link as resolving.
func Check(frontmatter map[string]any, body string, resolves func(string) bool, rules Rules) []Issue {
	var issues []Issue

	switch t := frontmatter["title"].(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			issues = append(issues, Issue{"error", "frontmatter `title` is empty"})
		}
	case nil:
		switch {
		case frontmatter != nil:
			issues = append(issues, Issue{"error", "frontmatter is missing a `title`"})
		case strings.HasPrefix(strings.TrimSpace(body), "---"):
			issues = append(issues, Issue{"error", "frontmatter is present but could not be parsed; check the YAML"})
		default:
			issues = append(issues, Issue{"error", "page has no frontmatter; add a `---` block with a `title`"})
		}
	default:
		issues = append(issues, Issue{"error", "frontmatter `title` must be text, not " + typeName(t)})
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
		if m[1] == "!" {
			continue // an embed is content, not a link to resolve
		}
		raw := strings.TrimSpace(m[2])
		display := raw
		if i := strings.IndexAny(display, "|#"); i >= 0 {
			display = strings.TrimSpace(display[:i])
		}
		if display == "" || seen[display] {
			continue
		}
		seen[display] = true
		if resolves == nil || resolves(raw) {
			continue
		}
		unresolved++
		if unresolved <= maxLinkWarnings {
			issues = append(issues, Issue{"warning", "wikilink [[" + display + "]] does not resolve to a known page"})
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

// typeName describes a non-string title value in plain terms, rather than
// leaking a Go type name into the lint message.
func typeName(v any) string {
	switch v.(type) {
	case []any:
		return "a list"
	case map[string]any, map[any]any:
		return "a mapping"
	case int, int64, float64:
		return "a number"
	case bool:
		return "true/false"
	default:
		return "a non-text value"
	}
}
