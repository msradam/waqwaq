package server

import (
	"testing"

	"github.com/msradam/waqwaq/internal/store"
)

func TestBuildNavTreeAndActivePath(t *testing.T) {
	metas := []store.PageMeta{
		{Slug: "guides/setup", Title: "Setup"},
		{Slug: "services/auth/overview", Title: "Overview"},
		{Slug: "services/auth/tokens", Title: "Tokens"},
		{Slug: "services/db", Title: "Database"},
	}
	nav := buildNav(metas, "services/auth/overview")

	services := find(nav, "services")
	if services == nil {
		t.Fatal("services folder missing")
	}
	if services.Slug != "" {
		t.Error("services should be a folder, not a page")
	}
	if !services.Open {
		t.Error("services should be open on the active path")
	}
	if len(services.Children) != 2 {
		t.Fatalf("services children = %d, want 2 (auth, db)", len(services.Children))
	}

	auth := find(services.Children, "auth")
	if auth == nil || !auth.Open {
		t.Fatal("auth folder should exist and be open")
	}
	overview := find(auth.Children, "overview")
	if overview == nil || !overview.Active || overview.Slug != "services/auth/overview" {
		t.Errorf("overview should be the active page, got %+v", overview)
	}
	if overview.Title != "Overview" {
		t.Errorf("overview title = %q", overview.Title)
	}

	if guides := find(nav, "guides"); guides == nil || guides.Open {
		t.Error("guides folder should exist and be closed off the active path")
	}
}

func find(nodes []*navNode, name string) *navNode {
	for _, n := range nodes {
		if n.Name == name {
			return n
		}
	}
	return nil
}
