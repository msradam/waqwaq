package core

import (
	"context"
	"time"
)

// Core is the stateless read interface for an OKF wiki.
// Every operation re-derives from the filesystem + git on each call.
// No caches, no indexes, no mutable state.
//
// There is deliberately no Write or Delete: this baseline is read-only across
// every surface. Reintroducing mutation is a separate baseline with its own
// review, not a stub left here "just in case".
type Core interface {
	// List returns pages, optionally filtered by prefix, type, or tag.
	// Returns pagination metadata (total count, truncated flag) for agent reasoning.
	List(ctx context.Context, prefix, typeFilter, tagFilter string, limit, offset int) (ListResult, error)

	// Read returns a page's full content (frontmatter + body).
	Read(ctx context.Context, slug string) (Page, error)

	// Search runs a literal or regex query over page bodies, optionally narrowed
	// by OKF type and tag, and returns matching pages with context snippets.
	Search(ctx context.Context, query string, isRegex bool, typeFilter, tagFilter string, limit int) ([]Match, error)

	// Backlinks returns pages that link to the given slug.
	Backlinks(ctx context.Context, slug string) ([]string, error)

	// Neighbors returns a page's N-degree neighbors in the link graph.
	Neighbors(ctx context.Context, slug string, depth int) ([]GraphNode, error)

	// Tags returns tag counts (optionally filtered by a specific tag).
	Tags(ctx context.Context, tag *string) ([]TagCount, error)

	// Hubs returns the most-connected pages (for entry points).
	Hubs(ctx context.Context, limit int) ([]GraphNode, error)

	// Recent returns concept pages ordered by OKF `timestamp` (freshest first);
	// pages without a timestamp sort last. Stateless, and works without git.
	Recent(ctx context.Context, limit int) ([]PageMeta, error)

	// History returns git commits touching a page.
	History(ctx context.Context, slug string, limit int) ([]Commit, error)

	// Graph returns the full page link graph.
	Graph(ctx context.Context) (Graph, error)
}

// ListResult wraps paginated list response with metadata.
type ListResult struct {
	Items     []PageMeta `json:"items"`
	Total     int        `json:"total"`     // count of all matching pages
	Truncated bool       `json:"truncated"` // whether results were truncated at limit
}

// PageMeta is minimal page metadata for list responses and graph nodes.
type PageMeta struct {
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Type        string   `json:"type,omitempty"`        // OKF type
	Description string   `json:"description,omitempty"` // OKF description
	Resource    string   `json:"resource,omitempty"`    // OKF resource URL
	Tags        []string `json:"tags,omitempty"`        // OKF tags
	Timestamp   string   `json:"timestamp,omitempty"`   // OKF timestamp (ISO 8601)
}

// Page is a full page with body content and inlined graph edges.
//
// Outbound and Inbound carry each neighbor's slug+title+type so an agent can
// decide where to navigate next without a read-per-neighbor round trip.
type Page struct {
	Slug        string         `json:"slug"`
	Title       string         `json:"title"`
	Frontmatter map[string]any `json:"frontmatter"`
	Body        string         `json:"body"` // markdown without frontmatter
	Outbound    []EdgeRef      `json:"outbound"`
	Inbound     []EdgeRef      `json:"inbound"`
}

// EdgeRef is a link target with just enough metadata to reason about it.
type EdgeRef struct {
	Slug  string `json:"slug"`
	Title string `json:"title,omitempty"`
	Type  string `json:"type,omitempty"`
}

// Match is a search result with context and OKF metadata, so results are
// decision-complete without a read-per-hit.
type Match struct {
	Slug    string   `json:"slug"`
	Title   string   `json:"title"`
	Type    string   `json:"type,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Context string   `json:"context"` // snippet around match
}

// TagCount is a tag with its page count.
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// GraphNode is a node in the link graph with metadata.
type GraphNode struct {
	Slug   string `json:"slug"`
	Title  string `json:"title"`
	Type   string `json:"type,omitempty"` // OKF type for coloring
	Degree int    `json:"degree"`         // undirected link count
}

// GraphEdge is a directed link from one page to another.
type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Graph is the full page link graph.
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// Commit is a git commit entry.
type Commit struct {
	Hash    string    `json:"hash"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
	Message string    `json:"message"`
}
