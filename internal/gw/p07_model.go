package gw

import (
	"context"

	"yunshu.dev/core/internal/apierr"
	obslog "yunshu.dev/core/internal/obs/log"
)

// ModelStage performs P07. The five checks run in a fixed order because the
// order decides which error the user sees, and therefore what the user does
// next (TDS §3.2 P07):
//
//  1. resolve model (name, then alias)          -> 400 model_not_found
//  2. listing_state CATALOG_ONLY                -> 503 model_not_available
//     listing_state DELISTED                    -> 400 model_not_found
//  3. Key authorization                         -> 403 model_not_authorized
//  4. group range                               -> 403 model_not_in_group
//  5. output model_id + default_price_rule_id
//
// Step 2 must precede authorization: a suspended model cannot be fixed by
// changing key scopes, so reporting 403 first sends the user through two
// useless actions before they contact support. Step 3 precedes step 4 for the
// same reason: the user can edit key scopes, but only an operator can change
// the price group.
type ModelStage struct{ d *Deps }

// NewModelStage builds P07.
func NewModelStage(d *Deps) *ModelStage { return &ModelStage{d: d} }

// Name implements Stage.
func (s *ModelStage) Name() string { return "P07" }

// Run implements Stage.
func (s *ModelStage) Run(ctx context.Context, c *Ctx) error {
	if c.IR == nil || c.Auth == nil {
		return StageErr("P07", apierr.New(apierr.CodeInternalError))
	}
	if s.d.Catalog == nil {
		return StageErr("P07", apierr.New(apierr.CodeConfigUnavailable))
	}
	snap, err := s.d.Catalog.Current()
	if err != nil {
		return StageErr("P07", apierr.New(apierr.CodeConfigUnavailable).WithCause(err))
	}

	// 1. Model resolution. Overseas models are treated as nonexistent: V1.0
	// must not route them even if a row was misconfigured into the database
	// (PRD §3.4).
	model := snap.ModelByName(c.IR.Model)
	if model == nil {
		model = snap.ModelByAlias(c.IR.Model)
	}
	if model == nil || model.Region != RegionDomestic {
		return StageErr("P07", apierr.Newf(apierr.CodeModelNotFound, c.IR.Model))
	}

	// 2. Listing state.
	switch model.ListingState {
	case ListingCatalogOnly:
		return StageErr("P07", apierr.Newf(apierr.CodeModelNotAvailable, model.ModelName))
	case ListingDelisted:
		return StageErr("P07", apierr.Newf(apierr.CodeModelNotFound, model.ModelName))
	case ListingListed:
	default:
		// An unknown state is not "listed". Treat it like DELISTED so a bad
		// enum cannot expose a model whose availability is unknown.
		return StageErr("P07", apierr.Newf(apierr.CodeModelNotFound, model.ModelName))
	}

	// 3. Key authorization.
	grant := snap.KeyGrant(c.Auth.KeyID)
	if _, present := snap.KeyModels[strKey(c.Auth.KeyID)]; !present {
		s.warn(ctx, "key_model_grant_missing: K13 key has no snapshot grant, denying")
	}
	if !grantAllows(grant, model.ID) {
		return StageErr("P07", apierr.Newf(apierr.CodeModelNotAuthorized, model.ModelName))
	}

	// 4. Group range. A missing group definition is a snapshot gap; deny
	// rather than silently granting every model to an unknown group.
	group, ok := snap.Group(c.Auth.GroupCode)
	if !ok {
		s.warn(ctx, "price_group_missing_in_snapshot: denying model")
		return StageErr("P07", apierr.Newf(apierr.CodeModelNotInGroup, model.ModelName))
	}
	if !group.HasModel(model.ID) {
		return StageErr("P07", apierr.Newf(apierr.CodeModelNotInGroup, model.ModelName))
	}

	c.Model = model
	return nil
}

func grantAllows(grant KeyModelGrant, modelID int64) bool {
	if grant.Mode == ScopeAll {
		return true
	}
	for _, id := range grant.ModelIDs {
		if id == modelID {
			return true
		}
	}
	return false
}

func (s *ModelStage) warn(ctx context.Context, msg string) {
	if s.d.Logger != nil {
		s.d.Logger.Warn(ctx, msg, obslog.Fields{Stage: "P07"})
	}
}
