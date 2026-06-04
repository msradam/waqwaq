// Package kbclient is the remote implementation of kb.KnowledgeBase. It carries
// no logic of its own: it forwards each call to a waqwaq server's /api and
// deserialises the IR, so a remote answer is identical to the local one by
// construction. The resolution, ranking, and link graph all stay computed once,
// on the server.
package kbclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/msradam/waqwaq/internal/kb"
	"github.com/msradam/waqwaq/internal/store"
)

var _ kb.KnowledgeBase = (*Client)(nil)

type Client struct {
	base  string
	token string
	http  *http.Client
}

func New(base, token string) *Client {
	return &Client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) get(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type ref struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

func metas(rs []ref) []store.PageMeta {
	out := make([]store.PageMeta, len(rs))
	for i, r := range rs {
		out[i] = store.PageMeta{Slug: r.Slug, Title: r.Title}
	}
	return out
}

func slugPath(slug string) string {
	parts := strings.Split(slug, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func (c *Client) List() ([]store.PageMeta, error) {
	var r struct {
		Pages []ref `json:"pages"`
	}
	if err := c.get("/api/pages", &r); err != nil {
		return nil, err
	}
	return metas(r.Pages), nil
}

func (c *Client) Read(slug string) (*store.Page, error) {
	var r struct {
		Slug        string         `json:"slug"`
		Title       string         `json:"title"`
		Frontmatter map[string]any `json:"frontmatter"`
		Body        string         `json:"body"`
	}
	if err := c.get("/api/page/"+slugPath(slug), &r); err != nil {
		return nil, err
	}
	return &store.Page{Slug: r.Slug, Title: r.Title, Frontmatter: r.Frontmatter, Body: r.Body, Raw: r.Body}, nil
}

func (c *Client) Search(query string) ([]store.SearchHit, error) {
	var r struct {
		Hits []store.SearchHit `json:"hits"`
	}
	if err := c.get("/api/search?q="+url.QueryEscape(query), &r); err != nil {
		return nil, err
	}
	return r.Hits, nil
}

func (c *Client) GraphView() (*store.GraphView, error) {
	var g store.GraphView
	if err := c.get("/api/graph", &g); err != nil {
		return nil, err
	}
	return &g, nil
}

func (c *Client) Neighbors(slug string, depth int) ([]store.Neighbor, error) {
	var r struct {
		Neighbors []store.Neighbor `json:"neighbors"`
	}
	if err := c.get("/api/neighbors/"+slugPath(slug)+"?depth="+strconv.Itoa(depth), &r); err != nil {
		return nil, err
	}
	return r.Neighbors, nil
}

func (c *Client) Path(from, to string) ([]store.PageMeta, error) {
	var r struct {
		Path []ref `json:"path"`
	}
	if err := c.get("/api/path?from="+url.QueryEscape(from)+"&to="+url.QueryEscape(to), &r); err != nil {
		return nil, err
	}
	return metas(r.Path), nil
}

func (c *Client) Hubs(limit int) ([]store.Hub, error) {
	var r struct {
		Hubs []store.Hub `json:"hubs"`
	}
	if err := c.get("/api/hubs?limit="+strconv.Itoa(limit), &r); err != nil {
		return nil, err
	}
	return r.Hubs, nil
}

func (c *Client) Backlinks(slug string) ([]store.PageMeta, error) {
	var r struct {
		Pages []ref `json:"pages"`
	}
	if err := c.get("/api/backlinks/"+slugPath(slug), &r); err != nil {
		return nil, err
	}
	return metas(r.Pages), nil
}

func (c *Client) Health() (*store.Health, error) {
	var h store.Health
	if err := c.get("/api/health", &h); err != nil {
		return nil, err
	}
	return &h, nil
}

func (c *Client) Tags() (map[string][]store.PageMeta, error) {
	var r struct {
		Tags map[string][]ref `json:"tags"`
	}
	if err := c.get("/api/tags", &r); err != nil {
		return nil, err
	}
	out := make(map[string][]store.PageMeta, len(r.Tags))
	for k, v := range r.Tags {
		out[k] = metas(v)
	}
	return out, nil
}

func (c *Client) ResolveLink(target string) (string, bool) {
	p, err := c.Read(target)
	if err != nil {
		return "", false
	}
	return p.Slug, true
}
