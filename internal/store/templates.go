package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const templatesDirName = "templates"

// ListTemplates returns the names of page templates under .waqwaq/templates.
func (s *Store) ListTemplates() []string {
	entries, err := os.ReadDir(filepath.Join(s.gitRoot, ".waqwaq", templatesDirName))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	sort.Strings(names)
	return names
}

// ReadTemplate returns a named template's content.
func (s *Store) ReadTemplate(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return "", fmt.Errorf("invalid template name %q", name)
	}
	data, err := os.ReadFile(filepath.Join(s.gitRoot, ".waqwaq", templatesDirName, name+".md"))
	return string(data), err
}
