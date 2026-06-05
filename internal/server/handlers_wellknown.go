package server

import (
	"encoding/json"
	"net/http"

	"github.com/crmkit/crmkit/internal/version"
)

// handleManualFile serves the operating manual as a Markdown file at the
// well-known agent path (<base_url>/.well-known/agent.md). Every URL inside is
// resolved against the running server's base_url, so this is the canonical
// "LLM file" to point an agent at - always correct for this deployment.
func (s *Server) handleManualFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(Manual(s.cfg.Server.BaseURL) + "\n"))
}

// agentCard is the machine-readable description served at
// /.well-known/agent.json - a compact pointer to the prose manual plus the auth
// scheme and capabilities, for clients that discover agents programmatically.
type agentCard struct {
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	Version        string    `json:"version"`
	BaseURL        string    `json:"base_url"`
	ManualURL      string    `json:"manual_url"`
	DocsURL        string    `json:"docs_url"`
	ContentTypes   []string  `json:"content_types"`
	Capabilities   []string  `json:"capabilities"`
	Authentication agentAuth `json:"authentication"`
}

// agentAuth describes how a client obtains a credential.
type agentAuth struct {
	Type            string `json:"type"`
	Scheme          string `json:"scheme"`
	Description     string `json:"description"`
	RequestEndpoint string `json:"request_endpoint"`
	VerifyEndpoint  string `json:"verify_endpoint"`
}

// handleAgentCard serves the JSON agent card. Its static fields come from the
// manual's frontmatter (single source of truth); only the base_url-dependent
// fields and the build version are filled in at request time.
func (s *Server) handleAgentCard(w http.ResponseWriter, r *http.Request) {
	base := s.cfg.Server.BaseURL
	m := Frontmatter()
	card := agentCard{
		Name:         m.Name,
		Description:  m.Description,
		Version:      version.Version,
		BaseURL:      base,
		ManualURL:    base + "/.well-known/agent.md",
		DocsURL:      base + "/help",
		ContentTypes: m.ContentTypes,
		Capabilities: m.Capabilities,
		Authentication: agentAuth{
			Type:            m.Authentication.Type,
			Scheme:          m.Authentication.Scheme,
			Description:     m.Authentication.Description,
			RequestEndpoint: base + "/auth/request",
			VerifyEndpoint:  base + "/auth/verify",
		},
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(card)
}
