package publicapi

import "net/http"

// This file holds the two public routes whose backing tables do not exist.
//
// TODO(T24): public access endpoints and integration deep links have no storage
// in V1.0. Verified against every migration under
// internal/platform/migrate/sql/mysql/: there is no t_endpoint /
// t_integration / t_tool table, and t_channel is an upstream pool with base_url
// and encrypted credentials — exposing it as a public access point would leak
// topology and invent data the product has not decided on.
//
// The frontend already treats an empty list as "render the generic fallback"
// (web/src/pages/site/IntegrationsPage.tsx); these handlers therefore return an
// honest empty array until T24 defines the tables and their operator surface.

// publicEndpoint is the wire shape the site expects once T24 lands. It is not
// populated in V1.0.
type publicEndpoint struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	BaseURL       string  `json:"base_url"`
	Line          string  `json:"line"`
	Description   string  `json:"description"`
	Role          string  `json:"role"`
	StatusPageURL string  `json:"status_page_url"`
	HealthStatus  *string `json:"health_status"`
}

type endpointsResponse struct {
	Data []publicEndpoint `json:"data"`
}

// publicIntegrationLink is the wire shape the site expects once T24 lands. The
// deep-link template is backend-owned: the browser never learns the parameter
// names (F4-2).
type publicIntegrationLink struct {
	ToolKey          string  `json:"tool_key"`
	DeepLinkTemplate *string `json:"deep_link_template"`
	Enabled          bool    `json:"enabled"`
}

type integrationLinksResponse struct {
	Data []publicIntegrationLink `json:"data"`
}

// Endpoints implements GET /api/public/endpoints. See the TODO(T24) above.
func (s *Service) Endpoints(w http.ResponseWriter, r *http.Request) {
	s.write(w, endpointsResponse{Data: []publicEndpoint{}})
}

// IntegrationLinks implements GET /api/public/integration-links. See the
// TODO(T24) above.
func (s *Service) IntegrationLinks(w http.ResponseWriter, r *http.Request) {
	s.write(w, integrationLinksResponse{Data: []publicIntegrationLink{}})
}
