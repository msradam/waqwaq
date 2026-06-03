// Package review is the proposal queue. Untrusted writes become pending
// proposals stored under .waqwaq/proposals, which a human approves or rejects in
// the web UI instead of committing straight to the wiki. A merge writes the page
// through the store, recording the proposer as git author and the approver in
// the commit message.
package review

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/msradam/waqwaq/internal/lint"
	"github.com/msradam/waqwaq/internal/store"
)

type Status string

const (
	Pending  Status = "pending"
	Merged   Status = "merged"
	Rejected Status = "rejected"
)

type Proposal struct {
	ID         string       `json:"id"`
	Slug       string       `json:"slug"`
	Title      string       `json:"title"`
	Content    string       `json:"content"`
	Author     string       `json:"author"`
	Created    time.Time    `json:"created"`
	BaseHash   string       `json:"base_hash"`   // hash of the page this was based on
	BaseExists bool         `json:"base_exists"` // whether the page existed when proposed
	Lint       []lint.Issue `json:"lint"`
	Status     Status       `json:"status"`
	Reviewer   string       `json:"reviewer,omitempty"`
	ReviewedAt *time.Time   `json:"reviewed_at,omitempty"`
	Reason     string       `json:"reason,omitempty"`
}

type Queue struct {
	st  *store.Store
	dir string
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

func New(st *store.Store) (*Queue, error) {
	dir := filepath.Join(st.Root(), ".waqwaq", "proposals")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Queue{st: st, dir: dir}, nil
}

// Create records a pending proposal for a page write.
func (q *Queue) Create(slug, content, author string, issues []lint.Issue) (*Proposal, error) {
	var baseExists bool
	var baseHash string
	switch base, err := q.st.Read(slug); {
	case err == nil:
		baseExists = true
		baseHash = hashContent(base.Raw)
	case errors.Is(err, os.ErrNotExist):
		// new page
	default:
		return nil, err // invalid slug or read error
	}

	fm, _ := store.SplitFrontmatter(content)
	title, _ := fm["title"].(string)
	if strings.TrimSpace(title) == "" {
		title = slug
	}

	p := &Proposal{
		ID:         newID(),
		Slug:       slug,
		Title:      title,
		Content:    content,
		Author:     author,
		Created:    time.Now().UTC(),
		BaseHash:   baseHash,
		BaseExists: baseExists,
		Lint:       issues,
		Status:     Pending,
	}
	if err := q.save(p); err != nil {
		return nil, err
	}
	return p, nil
}

func (q *Queue) List() ([]*Proposal, error) {
	entries, err := os.ReadDir(q.dir)
	if err != nil {
		return nil, err
	}
	var ps []*Proposal
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		p, err := q.load(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool {
		if pi, pj := ps[i].Status == Pending, ps[j].Status == Pending; pi != pj {
			return pi // pending first
		}
		return ps[i].Created.After(ps[j].Created)
	})
	return ps, nil
}

func (q *Queue) PendingCount() (int, error) {
	ps, err := q.List()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range ps {
		if p.Status == Pending {
			n++
		}
	}
	return n, nil
}

func (q *Queue) Get(id string) (*Proposal, error) { return q.load(id) }

// Stale reports whether the page the proposal was based on has changed since.
func (q *Queue) Stale(p *Proposal) bool {
	base, err := q.st.Read(p.Slug)
	if errors.Is(err, os.ErrNotExist) {
		return p.BaseExists
	}
	if err != nil {
		return false
	}
	return hashContent(base.Raw) != p.BaseHash
}

// Merge writes the proposed content to the wiki and marks the proposal merged.
func (q *Queue) Merge(id, reviewer string) (*Proposal, error) {
	p, err := q.load(id)
	if err != nil {
		return nil, err
	}
	if p.Status != Pending {
		return nil, fmt.Errorf("proposal %s is already %s", id, p.Status)
	}
	author := fmt.Sprintf("%s <proposed@waqwaq.local>", p.Author)
	message := fmt.Sprintf("waqwaq: merge proposal %s (%s), approved by %s", p.ID, p.Slug, reviewer)
	if err := q.st.Write(p.Slug, p.Content, author, message); err != nil {
		return nil, err
	}
	return q.close(p, Merged, reviewer, "")
}

func (q *Queue) Reject(id, reviewer, reason string) (*Proposal, error) {
	p, err := q.load(id)
	if err != nil {
		return nil, err
	}
	if p.Status != Pending {
		return nil, fmt.Errorf("proposal %s is already %s", id, p.Status)
	}
	return q.close(p, Rejected, reviewer, reason)
}

func (q *Queue) close(p *Proposal, status Status, reviewer, reason string) (*Proposal, error) {
	now := time.Now().UTC()
	p.Status = status
	p.Reviewer = reviewer
	p.ReviewedAt = &now
	p.Reason = reason
	if err := q.save(p); err != nil {
		return nil, err
	}
	return p, nil
}

func (q *Queue) load(id string) (*Proposal, error) {
	if !idRe.MatchString(id) {
		return nil, fmt.Errorf("invalid proposal id %q", id)
	}
	data, err := os.ReadFile(filepath.Join(q.dir, id+".json"))
	if err != nil {
		return nil, err
	}
	var p Proposal
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (q *Queue) save(p *Proposal) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(q.dir, p.ID+".json"), data, 0o644)
}

func hashContent(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("p-%d-%s", time.Now().UTC().UnixNano(), hex.EncodeToString(b))
}
