package publicapi

import (
	"database/sql"
	"net/http"
	"strconv"
	"time"
)

// publicModel mirrors web/src/api/public.ts PublicModel.
//
// The price fields come from the price rule active at request time, not from
// t_model.default_price_rule_id: the latter is only a pointer and may lag a
// version change. Models with no active rule are omitted entirely (F16-7 — the
// public catalogue never shows an unpriced model, and never shows a fake 0).
type publicModel struct {
	ID                   string    `json:"id"`
	ModelName            string    `json:"model_name"`
	DisplayName          string    `json:"display_name"`
	ModelType            string    `json:"model_type"`
	ListingState         string    `json:"listing_state"`
	ContextWindow        int64     `json:"context_window"`
	InputPriceMicroPerK  int64     `json:"input_price_micro_per_k"`
	OutputPriceMicroPerK int64     `json:"output_price_micro_per_k"`
	Protocols            []string  `json:"protocols"`
	RecommendWeight      int64     `json:"recommend_weight"`
	PublishedAt          time.Time `json:"published_at"`
}

type modelsResponse struct {
	Data []publicModel `json:"data"`
}

// Models implements GET /api/public/models.
//
// The effective-price subquery is the same rule as ops.activePriceRule and
// boot.RefreshPrices: enabled MODEL-scope row, effective_from <= now,
// effective_to NULL or in the future, highest (version, id) wins. It is written
// once here with a JOIN so the public list stays a single query.
func (s *Service) Models(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT m.id, m.model_name, m.display_name, m.model_type, m.listing_state,
		       m.context_length, m.recommend_weight,
		       COALESCE(m.published_at, m.created_at) AS published_at,
		       p.code,
		       pr.in_price_micro_per_k, pr.out_price_micro_per_k
		  FROM t_model m
		  LEFT JOIN t_protocol p ON p.id = m.protocol_id
		  JOIN t_price_rule pr
		    ON pr.scope_level = 'MODEL' AND pr.scope_id = m.id AND pr.enabled = 1
		   AND pr.effective_from <= ?
		   AND (pr.effective_to IS NULL OR pr.effective_to > ?)
		   AND pr.id = (
		       SELECT p2.id FROM t_price_rule p2
		        WHERE p2.scope_level = 'MODEL' AND p2.scope_id = m.id AND p2.enabled = 1
		          AND p2.effective_from <= ?
		          AND (p2.effective_to IS NULL OR p2.effective_to > ?)
		        ORDER BY p2.version DESC, p2.id DESC LIMIT 1)
		 WHERE m.listing_state = 'LISTED' AND m.deleted_at IS NULL
		 ORDER BY m.recommend_weight DESC, m.published_at DESC, m.id DESC`,
		now, now, now, now)
	if err != nil {
		s.fail(w, r, "public_models_failed: "+err.Error())
		return
	}
	defer rows.Close()

	data := make([]publicModel, 0, 16)
	for rows.Next() {
		var (
			m                 publicModel
			id                int64
			published         time.Time
			protocol          sql.NullString
			inPrice, outPrice sql.NullInt64
		)
		if err := rows.Scan(&id, &m.ModelName, &m.DisplayName, &m.ModelType,
			&m.ListingState, &m.ContextWindow, &m.RecommendWeight, &published,
			&protocol, &inPrice, &outPrice); err != nil {
			s.fail(w, r, "public_models_scan_failed: "+err.Error())
			return
		}
		// A NULL price column is treated like a missing rule: skip instead of
		// publishing 0, which would read as "free".
		if !inPrice.Valid || !outPrice.Valid {
			continue
		}
		m.ID = strconv.FormatInt(id, 10)
		m.PublishedAt = published.UTC()
		m.Protocols = publicProtocols(protocol)
		m.InputPriceMicroPerK = inPrice.Int64
		m.OutputPriceMicroPerK = outPrice.Int64
		data = append(data, m)
	}
	if err := rows.Err(); err != nil {
		s.fail(w, r, "public_models_rows_failed: "+err.Error())
		return
	}
	s.write(w, modelsResponse{Data: data})
}

// publicProtocols maps an upstream protocol code to the two names the site
// exposes (PublicModel.protocols is typed 'OPENAI' | 'ANTHROPIC').
//
// An unmapped code — BAILIAN/CUSTOM, or a model whose protocol_id points at no
// row — yields an empty array rather than a guessed label.
func publicProtocols(code sql.NullString) []string {
	if !code.Valid {
		return []string{}
	}
	switch code.String {
	case "OPENAI_COMPAT":
		return []string{"OPENAI"}
	case "ANTHROPIC_COMPAT":
		return []string{"ANTHROPIC"}
	}
	return []string{}
}
