package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/webitel/webitel-go-kit/infra/webitel/auth"
	"github.com/webitel/webitel-go-kit/pkg/errors"

	"github.com/webitel/webitel-audit/internal/model"
	"github.com/webitel/webitel-audit/internal/store"
)

const (
	MaskName             = "name"
	MaskDescription      = "description"
	MaskEnabled          = "enabled"
	MaskArchive          = "archive"
	MaskInteractionTypes = "interaction_types"
	MaskTeams            = "teams"
	MaskGroups           = "groups"
	MaskCriteria         = "criteria"
)

var errNoSession = errors.Unauthenticated("no session in the request", errors.WithID("audit.session.missing"))

// ScorecardService applies the scorecard rules; the caller's domain and user always come from the session.
type ScorecardService struct {
	store store.ScorecardStore
}

func NewScorecardService(s store.ScorecardStore) *ScorecardService {
	return &ScorecardService{store: s}
}

func (s *ScorecardService) Create(ctx context.Context, in *model.Scorecard) (*store.ScorecardView, error) {
	sess, err := session(ctx)
	if err != nil {
		return nil, err
	}

	in.CreatedBy = sess.UserID
	in.UpdatedBy = sess.UserID

	if err := normalize(in); err != nil {
		return nil, err
	}

	return s.store.Create(ctx, sess.DomainID, in)
}

func (s *ScorecardService) Read(ctx context.Context, id int32) (*store.ScorecardView, error) {
	sess, err := session(ctx)
	if err != nil {
		return nil, err
	}

	return s.store.Read(ctx, sess.DomainID, id, rbac(ctx, sess))
}

func (s *ScorecardService) Search(ctx context.Context, params *store.ScorecardSearch) ([]*store.ScorecardView, bool, error) {
	sess, err := session(ctx)
	if err != nil {
		return nil, false, err
	}

	// team_filter is a flag: the user always comes from the session, never from the request.
	params.TeamUserID = 0
	if params.TeamFilter {
		params.TeamUserID = sess.UserID
	}

	return s.store.Search(ctx, sess.DomainID, params, rbac(ctx, sess))
}

func (s *ScorecardService) Update(ctx context.Context, in *model.Scorecard) (*store.ScorecardView, error) {
	return s.Patch(ctx, in, maskNames)
}

// Patch applies the mask fields of in to the current scorecard in.ID under one row lock.
func (s *ScorecardService) Patch(ctx context.Context, in *model.Scorecard, mask []string) (*store.ScorecardView, error) {
	sess, err := session(ctx)
	if err != nil {
		return nil, err
	}

	return s.store.Patch(ctx, sess.DomainID, in.ID, rbac(ctx, sess), func(next *model.Scorecard, editable bool) error {
		base := next.Clone()
		base.Normalize()

		applyMask(next, in, mask)
		next.UpdatedBy = sess.UserID

		if err := normalize(next); err != nil {
			return err
		}

		if !editable && !base.StructureEqual(next) {
			return model.ErrNotEditable
		}

		return nil
	})
}

func (s *ScorecardService) Delete(ctx context.Context, id int32) (*store.ScorecardView, error) {
	sess, err := session(ctx)
	if err != nil {
		return nil, err
	}

	return s.store.Delete(ctx, sess.DomainID, id, rbac(ctx, sess))
}

var maskNames = []string{
	MaskName, MaskDescription, MaskEnabled, MaskArchive, MaskInteractionTypes, MaskTeams, MaskGroups, MaskCriteria,
}

// applyMask copies the mask fields of src to dst; unknown names are skipped.
func applyMask(dst, src *model.Scorecard, mask []string) {
	dropped := make(map[uuid.UUID]bool, len(dst.Groups))
	for _, g := range dst.Groups {
		dropped[g.ID] = true
	}

	for _, name := range mask {
		switch name {
		case MaskName:
			dst.Name = src.Name
		case MaskDescription:
			dst.Description = src.Description
		case MaskEnabled:
			dst.Enabled = src.Enabled
		case MaskArchive:
			dst.Archive = src.Archive
		case MaskInteractionTypes:
			dst.InteractionTypes = src.InteractionTypes
		case MaskTeams:
			dst.TeamIDs = src.TeamIDs
		case MaskGroups:
			dst.Groups = src.Groups
		case MaskCriteria:
			dst.Criteria = src.Criteria
		}
	}

	for _, g := range dst.Groups {
		delete(dropped, g.ID)
	}

	for i := range dst.Criteria {
		if dropped[dst.Criteria[i].GroupID] {
			dst.Criteria[i].GroupID = uuid.Nil
		}
	}
}

func normalize(in *model.Scorecard) error {
	in.Normalize()

	return in.Validate()
}

func session(ctx context.Context) (*auth.Session, error) {
	// A session without a user cannot be recorded as created_by/updated_by.
	sess, ok := auth.SessionFromContext(ctx)
	if !ok || sess.UserID == 0 {
		return nil, errNoSession
	}

	return sess, nil
}

// rbac limits the call to the caller's ACL roles unless the session holds the class-wide permission.
func rbac(ctx context.Context, sess *auth.Session) *store.RBAC {
	if !auth.RequiresRBAC(ctx) {
		return nil
	}

	return &store.RBAC{Roles: sess.ACLRoles()}
}
