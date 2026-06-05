package server

import (
	_ "embed"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// manualTemplate is the agent operating manual, embedded from agent.md (the
// single source of truth - edit that markdown file, not a Go string). The body
// uses the literal placeholder "<base_url>", substituted per request in Manual.
//
//go:embed agent.md
var manualTemplate string

// Manual returns the agent operating manual served at GET /help and
// /.well-known/agent.md. It begins with YAML frontmatter (the single source of
// truth for the agent card's static metadata) followed by the prose manual a
// model reads. The frontmatter has no base_url-dependent fields; the prose body
// interpolates base_url by replacing the "<base_url>" placeholder.
func Manual(baseURL string) string {
	return strings.TrimSpace(strings.ReplaceAll(manualTemplate, "<base_url>", baseURL))
}

// ManualMeta is the structured metadata parsed from the manual's frontmatter.
type ManualMeta struct {
	Name           string   `yaml:"name"`
	Description    string   `yaml:"description"`
	ContentTypes   []string `yaml:"content_types"`
	Capabilities   []string `yaml:"capabilities"`
	Authentication struct {
		Type        string `yaml:"type"`
		Scheme      string `yaml:"scheme"`
		Description string `yaml:"description"`
	} `yaml:"authentication"`
}

var (
	metaOnce sync.Once
	meta     ManualMeta
)

// Frontmatter parses (once) and returns the manual's frontmatter metadata. It is
// the single source of truth shared by the manual and the JSON agent card.
func Frontmatter() ManualMeta {
	metaOnce.Do(func() {
		front, _ := splitFrontmatter(manualTemplate)
		_ = yaml.Unmarshal([]byte(front), &meta)
	})
	return meta
}

// splitFrontmatter separates a leading "---\n...\n---" YAML block from the body.
// If there is no frontmatter it returns an empty front and the whole input.
func splitFrontmatter(doc string) (front, body string) {
	doc = strings.TrimLeft(doc, "\n")
	if !strings.HasPrefix(doc, "---\n") {
		return "", doc
	}
	rest := doc[len("---\n"):]
	if i := strings.Index(rest, "\n---"); i >= 0 {
		return rest[:i], strings.TrimLeft(rest[i+len("\n---"):], "\n")
	}
	return "", doc
}
