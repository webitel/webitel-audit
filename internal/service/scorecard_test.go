package service

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"

	authpb "buf.build/gen/go/webitel/webitel-go/protocolbuffers/go"

	"github.com/webitel/webitel-go-kit/infra/webitel/auth"
	"github.com/webitel/webitel-go-kit/pkg/errors"

	auditv1 "github.com/webitel/webitel-audit/gen/go/audit/v1"
	"github.com/webitel/webitel-audit/gen/mocks"
	"github.com/webitel/webitel-audit/internal/model"
	"github.com/webitel/webitel-audit/internal/store"
)

const (
	testDomain = int64(1)
	testUser   = int64(2)
	testRole   = int64(10)
)

// fakeUserInfo is go.webitel.app returning one fixed session.
type fakeUserInfo struct{ info *authpb.Userinfo }

func (f fakeUserInfo) UserInfo(context.Context, *authpb.UserinfoRequest, ...grpc.CallOption) (*authpb.Userinfo, error) {
	return f.info, nil
}

// fakeMethodStream gives the context the method name grpc.Method reads inside a server.
type fakeMethodStream struct{ method string }

func (f fakeMethodStream) Method() string             { return f.method }
func (fakeMethodStream) SetHeader(metadata.MD) error  { return nil }
func (fakeMethodStream) SendHeader(metadata.MD) error { return nil }
func (fakeMethodStream) SetTrailer(metadata.MD) error { return nil }

// callCtx returns the context the auth interceptor hands to a handler of method; with super the
// session holds the class-wide permissions, so no record-level check is required.
func callCtx(t *testing.T, method string, super bool) context.Context {
	t.Helper()

	info := &authpb.Userinfo{
		Dc: testDomain, UserId: testUser,
		Roles:   []*authpb.ObjectId{{Id: testRole}},
		License: []*authpb.LicenseUser{{Prod: "QUALITY_AUDITOR"}},
		Scope:   []*authpb.Objclass{{Class: "cc_audit_form", Rbac: true}},
	}
	if super {
		info.Permissions = []*authpb.Permission{{Id: "read"}, {Id: "write"}, {Id: "delete"}}
	}

	client := auth.New(fakeUserInfo{info}, auth.WithCacheTTL(0))

	t.Cleanup(func() { _ = client.Close() })

	authorizer, err := auth.NewAuthorizer(client, auditv1.WebitelAPI)
	require.NoError(t, err)

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(auth.TokenHeader, "token"))
	ctx, err = authorizer.Authorize(grpc.NewContextWithServerTransportStream(ctx, fakeMethodStream{method}))
	require.NoError(t, err)

	return ctx
}

func newService(t *testing.T) (*ScorecardService, *mocks.MockScorecardStore) {
	t.Helper()

	m := mocks.NewMockScorecardStore(gomock.NewController(t))

	return NewScorecardService(m), m
}

func validScorecard() *model.Scorecard {
	return &model.Scorecard{
		DomainID:         99, // must be ignored: the domain comes from the session
		Name:             "Quality",
		Enabled:          true,
		InteractionTypes: []model.InteractionType{model.InteractionTypeCall},
		Criteria: []model.Criterion{
			{Type: model.CriterionTypeScore, Question: "Greeting", Required: true, Min: 0, Max: 5},
		},
	}
}

func TestCreate(t *testing.T) {
	svc, m := newService(t)
	ctx := callCtx(t, auditv1.ScorecardService_CreateScorecard_FullMethodName, false)

	m.EXPECT().Create(ctx, testDomain, gomock.Any()).DoAndReturn(
		func(_ context.Context, _ int64, in *model.Scorecard) (*store.ScorecardView, error) {
			assert.Equal(t, testUser, in.CreatedBy)
			assert.Equal(t, testUser, in.UpdatedBy)
			assert.NotEqual(t, uuid.Nil, in.Criteria[0].ID, "normalized before the store")
			assert.InDelta(t, 1, in.Criteria[0].Weight, 0)

			return &store.ScorecardView{Scorecard: *in}, nil
		},
	)

	_, err := svc.Create(ctx, validScorecard())
	require.NoError(t, err)
}

func TestInvalidScorecardNeverReachesStore(t *testing.T) {
	// The mock has no expectations: any store call fails the test.
	svc, _ := newService(t)

	bad := validScorecard()
	bad.Name = "ab"

	_, err := svc.Create(callCtx(t, auditv1.ScorecardService_CreateScorecard_FullMethodName, false), bad)
	assert.Equal(t, codes.InvalidArgument, errors.Code(err))
}

func TestNoSessionNeverReachesStore(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"no session": context.Background(),
		// A session without a user cannot fill created_by/updated_by (NOT NULL).
		"session without user": auth.ContextWithSession(context.Background(), &auth.Session{DomainID: testDomain}),
	} {
		t.Run(name, func(t *testing.T) {
			// The mock has no expectations: any store call fails the test.
			svc, _ := newService(t)

			_, err := svc.Create(ctx, validScorecard())
			assert.Equal(t, codes.Unauthenticated, errors.Code(err), "create")

			_, err = svc.Read(ctx, 1)
			assert.Equal(t, codes.Unauthenticated, errors.Code(err), "read")

			_, _, err = svc.Search(ctx, &store.ScorecardSearch{})
			assert.Equal(t, codes.Unauthenticated, errors.Code(err), "search")

			upd := validScorecard()
			upd.ID = 1
			_, err = svc.Update(ctx, upd)
			assert.Equal(t, codes.Unauthenticated, errors.Code(err), "update")

			_, err = svc.Patch(ctx, &model.Scorecard{ID: 1, Name: "Renamed"}, []string{MaskName})
			assert.Equal(t, codes.Unauthenticated, errors.Code(err), "patch")

			_, err = svc.Delete(ctx, 1)
			assert.Equal(t, codes.Unauthenticated, errors.Code(err), "delete")
		})
	}
}

func TestRBACOnlyWhenRequired(t *testing.T) {
	limited := &store.RBAC{Roles: []int64{testUser, testRole}}

	for _, tt := range []struct {
		name  string
		super bool
		want  *store.RBAC
	}{
		{"record-level check", false, limited},
		{"class-wide permission", true, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc, m := newService(t)
			params := &store.ScorecardSearch{Q: "x"}

			m.EXPECT().Read(gomock.Any(), testDomain, int32(3), tt.want).Return(&store.ScorecardView{}, nil)
			m.EXPECT().Search(gomock.Any(), testDomain, params, tt.want).Return(nil, false, nil)
			m.EXPECT().Delete(gomock.Any(), testDomain, int32(3), tt.want).Return(&store.ScorecardView{}, nil)
			// Update is a Patch of every field.
			m.EXPECT().Patch(gomock.Any(), testDomain, int32(3), tt.want, gomock.Any()).Return(&store.ScorecardView{}, nil).Times(2)

			_, err := svc.Read(callCtx(t, auditv1.ScorecardService_ReadScorecard_FullMethodName, tt.super), 3)
			require.NoError(t, err)

			_, _, err = svc.Search(callCtx(t, auditv1.ScorecardService_SearchScorecards_FullMethodName, tt.super), params)
			require.NoError(t, err)

			_, err = svc.Delete(callCtx(t, auditv1.ScorecardService_DeleteScorecard_FullMethodName, tt.super), 3)
			require.NoError(t, err)

			upd := validScorecard()
			upd.ID = 3
			_, err = svc.Update(callCtx(t, auditv1.ScorecardService_UpdateScorecard_FullMethodName, tt.super), upd)
			require.NoError(t, err)

			_, err = svc.Patch(callCtx(t, auditv1.ScorecardService_PatchScorecard_FullMethodName, tt.super),
				&model.Scorecard{ID: 3}, []string{MaskName})
			require.NoError(t, err)
		})
	}
}

// current is a stored editable form with one group and one criterion in it.
func current() *store.ScorecardView {
	group := uuid.New()
	sc := validScorecard()
	sc.ID = 3
	sc.DomainID = testDomain
	sc.Description = "old description"
	sc.TeamIDs = []int64{7}
	sc.CreatedBy = 5
	sc.Editable = true
	sc.Groups = []model.CriteriaGroup{{ID: group, Name: "Opening"}}
	sc.Criteria[0].ID = uuid.New()
	sc.Criteria[0].GroupID = group
	sc.Criteria[0].Weight = 2

	return &store.ScorecardView{Scorecard: *sc}
}

// patchOn makes the store mock run the service callback on was, as the store does under its row lock.
func patchOn(m *mocks.MockScorecardStore, ctx context.Context, was *store.ScorecardView, check func(*model.Scorecard)) {
	m.EXPECT().Patch(ctx, testDomain, was.ID, gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ int64, _ int32, _ *store.RBAC, mutate func(*model.Scorecard, bool) error) (*store.ScorecardView, error) {
			next := was.Clone()
			if err := mutate(&next, was.Editable); err != nil {
				return nil, err
			}

			if check != nil {
				check(&next)
			}

			return &store.ScorecardView{Scorecard: next}, nil
		},
	)
}

func TestPatchAppliesOnlyMaskedFields(t *testing.T) {
	svc, m := newService(t)
	ctx := callCtx(t, auditv1.ScorecardService_PatchScorecard_FullMethodName, false)
	was := current()

	// The request carries other values everywhere; only name is in the mask.
	in := &model.Scorecard{ID: 3, Name: "  New name ", Description: "ignored", TeamIDs: []int64{8}}

	patchOn(m, ctx, was, func(got *model.Scorecard) {
		assert.Equal(t, "New name", got.Name, "masked and normalized")
		assert.Equal(t, "old description", got.Description)
		assert.Equal(t, []int64{7}, got.TeamIDs)
		assert.Equal(t, was.Groups, got.Groups)
		assert.Equal(t, was.Criteria, got.Criteria, "children kept when not in the mask")
		assert.Equal(t, int64(5), got.CreatedBy)
		assert.Equal(t, testUser, got.UpdatedBy)
	})

	_, err := svc.Patch(ctx, in, []string{MaskName})
	require.NoError(t, err)
}

func TestPatchReplacesChildrenInMask(t *testing.T) {
	svc, m := newService(t)
	ctx := callCtx(t, auditv1.ScorecardService_PatchScorecard_FullMethodName, false)

	in := &model.Scorecard{ID: 3, Criteria: []model.Criterion{
		{Type: model.CriterionTypeYes, Question: "Polite"},
		{Type: model.CriterionTypeScore, Question: "Solved", Required: true, Max: 3},
	}}

	patchOn(m, ctx, current(), func(got *model.Scorecard) {
		require.Len(t, got.Criteria, 2, "the whole set is replaced")
		assert.Equal(t, "Polite", got.Criteria[0].Question)
		assert.Len(t, got.Groups, 1, "groups not in the mask stay")
		assert.Equal(t, "Quality", got.Name)
	})

	_, err := svc.Patch(ctx, in, []string{MaskCriteria})
	require.NoError(t, err)
}

// Confluence "Е2. Scorecards", AC_02.09.09: criteria of a deleted group stay in the form without a group.
func TestPatchDroppedGroupUngroupsCriteria(t *testing.T) {
	svc, m := newService(t)
	ctx := callCtx(t, auditv1.ScorecardService_PatchScorecard_FullMethodName, false)
	was := current()

	patchOn(m, ctx, was, func(got *model.Scorecard) {
		assert.Empty(t, got.Groups)
		require.Len(t, got.Criteria, 1)
		assert.Equal(t, was.Criteria[0].ID, got.Criteria[0].ID, "the criterion stays")
		assert.Equal(t, uuid.Nil, got.Criteria[0].GroupID)
	})

	_, err := svc.Patch(ctx, &model.Scorecard{ID: 3}, []string{MaskGroups})
	require.NoError(t, err)
}

func TestPatchRejectsUnknownGroupOfNewCriteria(t *testing.T) {
	svc, m := newService(t)
	ctx := callCtx(t, auditv1.ScorecardService_PatchScorecard_FullMethodName, false)

	in := &model.Scorecard{ID: 3, Criteria: []model.Criterion{
		{Type: model.CriterionTypeScore, Question: "Solved", Required: true, Max: 3, GroupID: uuid.New()},
	}}

	patchOn(m, ctx, current(), nil)

	_, err := svc.Patch(ctx, in, []string{MaskCriteria})
	assert.Equal(t, "audit.scorecard.criterion.group", errors.ID(err), "an id that never was a group still fails")
}

// Confluence "Е2. Scorecards", AC_02.09.09: deleting a group leaves its criteria in the form without a group;
// the frontend sends PUT only, so Update ungroups them as Patch does.
func TestUpdateDroppedGroupUngroupsCriteria(t *testing.T) {
	svc, m := newService(t)
	ctx := callCtx(t, auditv1.ScorecardService_UpdateScorecard_FullMethodName, false)
	was := current()

	in := was.Scorecard
	in.Groups = nil
	in.Criteria = slices.Clone(was.Criteria) // still points at the dropped group

	patchOn(m, ctx, was, func(got *model.Scorecard) {
		assert.Empty(t, got.Groups)
		require.Len(t, got.Criteria, 1)
		assert.Equal(t, was.Criteria[0].ID, got.Criteria[0].ID, "the criterion stays")
		assert.Equal(t, uuid.Nil, got.Criteria[0].GroupID)
		assert.Equal(t, testUser, got.UpdatedBy)
	})

	_, err := svc.Update(ctx, &in)
	require.NoError(t, err)
}

func TestPatchInvalidAfterMerge(t *testing.T) {
	svc, m := newService(t)
	ctx := callCtx(t, auditv1.ScorecardService_PatchScorecard_FullMethodName, false)

	// A nil check: if the merged form passed, the store would return no error and the assert below fails.
	patchOn(m, ctx, current(), nil)

	// Confluence "Е2. Scorecards", US_2.4: a form needs at least one interaction type.
	_, err := svc.Patch(ctx, &model.Scorecard{ID: 3}, []string{MaskInteractionTypes})
	assert.Equal(t, codes.InvalidArgument, errors.Code(err))
}

// Confluence "Е2. Scorecards", AC_02.12.01/AC_02.12.02: on a form with evaluations (editable = false) only
// descriptive fields change; a structural change is rejected before the store writes anything.
func TestPatchFrozenStructure(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mask   []string
		in     func(was *store.ScorecardView) *model.Scorecard
		wantID string // empty: allowed
	}{
		{
			"descriptive change allowed",
			[]string{MaskName, MaskDescription, MaskTeams, MaskGroups, MaskCriteria},
			func(was *store.ScorecardView) *model.Scorecard {
				in := was.Clone()
				in.Name, in.Description, in.TeamIDs = "Renamed", "new", []int64{8}
				in.Groups[0].Name = "Start"
				in.Criteria[0].Description = "hint"

				return &in
			}, "",
		},
		{
			"criterion weight is frozen",
			[]string{MaskCriteria},
			func(was *store.ScorecardView) *model.Scorecard {
				in := was.Clone()
				in.Criteria[0].Weight = 3

				return &in
			}, "audit.scorecard.not_editable",
		},
		{
			"interaction type is frozen",
			[]string{MaskInteractionTypes},
			func(*store.ScorecardView) *model.Scorecard {
				return &model.Scorecard{ID: 3, InteractionTypes: []model.InteractionType{model.InteractionTypeChat}}
			}, "audit.scorecard.not_editable",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc, m := newService(t)
			ctx := callCtx(t, auditv1.ScorecardService_PatchScorecard_FullMethodName, false)
			was := current()
			was.Editable = false

			patchOn(m, ctx, was, nil)

			_, err := svc.Patch(ctx, tt.in(was), tt.mask)
			if tt.wantID == "" {
				require.NoError(t, err)

				return
			}

			assert.Equal(t, codes.PermissionDenied, errors.Code(err))
			assert.Equal(t, tt.wantID, errors.ID(err))
		})
	}
}

func TestSearchTeamFilterUsesSessionUser(t *testing.T) {
	svc, m := newService(t)
	ctx := callCtx(t, auditv1.ScorecardService_SearchScorecards_FullMethodName, true)

	m.EXPECT().Search(ctx, testDomain, &store.ScorecardSearch{TeamFilter: true, TeamUserID: testUser}, nil).Return(nil, false, nil)
	m.EXPECT().Search(ctx, testDomain, &store.ScorecardSearch{}, nil).Return(nil, false, nil)

	_, _, err := svc.Search(ctx, &store.ScorecardSearch{TeamFilter: true, TeamUserID: 999})
	require.NoError(t, err)

	_, _, err = svc.Search(ctx, &store.ScorecardSearch{TeamUserID: 999})
	require.NoError(t, err, "a user id from the request never filters")
}
