package render

import (
	"strings"
	"testing"
)

func TestCalloutAndTOC(t *testing.T) {
	r := New("")
	md := "## Setup\n\n> [!WARNING]\n> Be careful.\n\n### Sub step\n\ntext\n"
	html, toc, err := r.Render(md)
	if err != nil {
		t.Fatal(err)
	}
	h := string(html)
	if !strings.Contains(h, `class="admonition adm-warning"`) {
		t.Errorf("callout class missing:\n%s", h)
	}
	if strings.Contains(h, "[!WARNING]") {
		t.Errorf("callout marker not stripped:\n%s", h)
	}
	if !strings.Contains(h, "Be careful.") {
		t.Errorf("callout body missing:\n%s", h)
	}
	if len(toc) != 2 || toc[0].ID != "setup" || toc[0].Level != 2 || toc[1].Level != 3 {
		t.Fatalf("toc = %+v, want Setup(h2) and Sub step(h3)", toc)
	}
}

func TestWikilinkBasePrefix(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"", `href="/wiki/runbook"`},
		{"/w/platform", `href="/w/platform/wiki/runbook"`},
	} {
		html, _, err := New(tc.base).Render("see [[runbook]]\n")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(html), tc.want) {
			t.Errorf("base %q: want %s in\n%s", tc.base, tc.want, html)
		}
	}
}

func TestPlainBlockquoteUntouched(t *testing.T) {
	r := New("")
	html, _, err := r.Render("> just a quote\n")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(html), "admonition") {
		t.Errorf("plain blockquote should not become a callout:\n%s", html)
	}
}
