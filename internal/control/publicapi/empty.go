package publicapi

import (
	"database/sql"
	"net/http"
)

// This file holds GET /api/public/endpoints, the one public route whose backing
// table does not exist, plus GET /api/public/integration-links.
//
// TODO(T24): public access endpoints have no storage in V1.0. Verified against
// every migration under internal/platform/migrate/sql/mysql/: there is no
// t_endpoint / t_tool table, and t_channel is an upstream pool with base_url
// and encrypted credentials — exposing it as a public access point would leak
// topology and invent data the product has not decided on. This route therefore
// still returns an honest empty array.
//
// t_integration exists since 0026 (T33) and backs integration-links: operators
// own the deep-link template, only enabled rows are exposed, and the template
// carries placeholders rather than credentials. The browser still never sees an
// upstream key (F4-2 / D-30).

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

// publicIntegrationLink is the wire shape the site expects. The deep-link
// template is backend-owned: the browser never learns the parameter names
// (F4-2). Disabled tools are not returned at all, so enabled is always true in
// the payload; it stays in the shape for the frontend contract.
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

// IntegrationLinks implements GET /api/public/integration-links. Only
// t_integration rows with enabled = 1 are returned; a NULL template stays JSON
// null rather than becoming an empty string.
func (s *Service) IntegrationLinks(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT tool_key, deep_link_template
		  FROM t_integration
		 WHERE enabled = 1
		 ORDER BY id`)
	if err != nil {
		s.fail(w, r, "public_integration_links_failed: "+err.Error())
		return
	}
	defer rows.Close()

	data := make([]publicIntegrationLink, 0, 5)
	for rows.Next() {
		var (
			link publicIntegrationLink
			tpl  sql.NullString
		)
		if err := rows.Scan(&link.ToolKey, &tpl); err != nil {
			s.fail(w, r, "public_integration_links_scan_failed: "+err.Error())
			return
		}
		if tpl.Valid {
			v := tpl.String
			link.DeepLinkTemplate = &v
		}
		link.Enabled = true
		data = append(data, link)
	}
	if err := rows.Err(); err != nil {
		s.fail(w, r, "public_integration_links_rows_failed: "+err.Error())
		return
	}
	s.write(w, integrationLinksResponse{Data: data})
}
