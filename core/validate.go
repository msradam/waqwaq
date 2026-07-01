package core

import "context"

// Report is the result of validating a bundle against the OKF spec.
//
// Compliance lists spec violations (SPEC §9): a non-reserved concept page whose
// frontmatter lacks a non-empty `type`. These are what "OKF compliant" means and
// what a server refuses to start on.
//
// BrokenLinks lists links whose target does not resolve. The spec (§9) says a
// conformant consumer MUST NOT reject a bundle for broken links, so these are
// reported as warnings and never block serving; they are still useful in CI.
type Report struct {
	Pages        int
	Compliance   []string
	BrokenLinks  []string
	StrictMisses []string // when strict: missing recommended fields (title/description/timestamp)
}

// OK reports whether the bundle is OKF compliant (no compliance violations).
func (r Report) OK() bool { return len(r.Compliance) == 0 }

// Validate checks a wiki against the OKF spec. strict additionally flags concept
// pages missing the recommended description and timestamp fields (part of the
// profile Google's reference agent emits), which the spec recommends but does
// not mandate. Title is not checked: it always resolves (frontmatter, else a
// humanized slug), so it is never absent.
func Validate(ctx context.Context, c Core, strict bool) (Report, error) {
	res, err := c.List(ctx, "", "", "", 1<<30, 0) // every concept page
	if err != nil {
		return Report{}, err
	}
	rep := Report{Pages: res.Total}

	slugSet := make(map[string]bool, len(res.Items))
	for _, p := range res.Items {
		slugSet[p.Slug] = true
	}
	for _, p := range res.Items {
		if p.Type == "" {
			rep.Compliance = append(rep.Compliance, p.Slug+": missing OKF type")
		}
		if strict {
			if p.Description == "" {
				rep.StrictMisses = append(rep.StrictMisses, p.Slug+": missing description")
			}
			if p.Timestamp == "" {
				rep.StrictMisses = append(rep.StrictMisses, p.Slug+": missing timestamp")
			}
		}
	}

	g, err := c.Graph(ctx)
	if err != nil {
		return Report{}, err
	}
	for _, e := range g.Edges {
		// Reserved index/log targets are valid destinations even when not concepts.
		if !slugSet[e.To] && !isReserved(e.To) {
			rep.BrokenLinks = append(rep.BrokenLinks, e.From+`: broken link to "`+e.To+`"`)
		}
	}
	return rep, nil
}
