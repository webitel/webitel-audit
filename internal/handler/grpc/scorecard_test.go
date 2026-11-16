package grpc

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"buf.build/go/protovalidate"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/webitel/webitel-go-kit/infra/webitel/auth"
	"github.com/webitel/webitel-go-kit/pkg/errors"

	auditv1 "github.com/webitel/webitel-audit/gen/go/audit/v1"
	"github.com/webitel/webitel-audit/gen/mocks"
	"github.com/webitel/webitel-audit/internal/model"
	"github.com/webitel/webitel-audit/internal/service"
	"github.com/webitel/webitel-audit/internal/store"
)

// fullView sets every field the API carries, each to a non-zero value.
func fullView() *store.ScorecardView {
	group := uuid.New()
	threshold := int16(2)

	return &store.ScorecardView{
		Scorecard: model.Scorecard{
			ID: 4, Name: "Quality", Description: "desc", Enabled: true, Archive: true, Editable: true,
			InteractionTypes: []model.InteractionType{model.InteractionTypeCall, model.InteractionTypeEmail},
			TeamIDs:          []int64{7},
			Groups:           []model.CriteriaGroup{{ID: group, Name: "Opening", Position: 0}},
			Criteria: []model.Criterion{
				{
					ID: uuid.New(), GroupID: group, Position: 0, Type: model.CriterionTypeScore, Question: "Greeting",
					Description: "say hi", Required: true, AllowNA: true, Min: -2, Max: 5,
					Weight: 2.5, RequireComment: true, CommentThreshold: &threshold,
				},
				{
					ID: uuid.New(), Position: 1, Type: model.CriterionTypeOption, Question: "Tone", Weight: 1,
					Options: []model.Option{
						{ID: uuid.New(), Position: 0, Name: "Good", Score: 1.5, RequireComment: false},
						{ID: uuid.New(), Position: 1, Name: "Bad", Score: -1, RequireComment: true},
					},
				},
				{ID: uuid.New(), Position: 2, Type: model.CriterionTypeYes, Question: "Polite", Weight: 1, CriticalViolation: true, RequireComment: true},
			},
			CreatedAt: time.UnixMilli(1_700_000_000_123),
			UpdatedAt: time.UnixMilli(1_700_000_100_456),
		},
		Creator: &store.Lookup{ID: 2, Name: "Ann"},
		Updater: &store.Lookup{ID: 3, Name: "Bob"},
		Teams:   []store.Lookup{{ID: 7, Name: "Support"}},
	}
}

func TestScorecardRoundTrip(t *testing.T) {
	view := fullView()

	pb := scorecardToProto(view)

	assert.Equal(t, int32(4), pb.GetId())
	assert.Equal(t, int64(1_700_000_000_123), pb.GetCreatedAt())
	assert.Equal(t, int64(1_700_000_100_456), pb.GetUpdatedAt())
	assert.Equal(t, "Ann", pb.GetCreatedBy().GetName())
	assert.Equal(t, int64(3), pb.GetUpdatedBy().GetId())
	assert.True(t, pb.GetEditable())
	assert.Equal(t, []*auditv1.Lookup{{Id: 7, Name: "Support"}}, pb.GetTeams())
	assert.Equal(t, []auditv1.InteractionType{
		auditv1.InteractionType_INTERACTION_TYPE_CALL, auditv1.InteractionType_INTERACTION_TYPE_EMAIL,
	}, pb.GetInteractionType())
	assert.Equal(t, auditv1.CriterionType_CRITERION_TYPE_SCORE, pb.GetCriteria()[0].GetType())
	assert.Equal(t, auditv1.CriterionType_CRITERION_TYPE_OPTION, pb.GetCriteria()[1].GetType())
	assert.Equal(t, auditv1.CriterionType_CRITERION_TYPE_YES, pb.GetCriteria()[2].GetType())
	assert.Equal(t, int32(2), pb.GetCriteria()[0].GetCommentThreshold())
	assert.Empty(t, pb.GetCriteria()[1].GetGroupId(), "no group is an empty id")

	// The request side maps everything back to the same model.
	back, err := scorecardFromProto(pb.GetName(), pb.GetDescription(), pb.GetEnabled(), pb.GetArchive(),
		pb.GetInteractionType(), pb.GetTeams(), pb.GetCriteriaGroups(), pb.GetCriteria())
	require.NoError(t, err)

	want := view.Scorecard
	want.ID, want.Editable, want.CreatedAt, want.UpdatedAt = 0, false, time.Time{}, time.Time{}

	back.Normalize()
	assert.Equal(t, want, *back)
}

func TestEnumJSONNames(t *testing.T) {
	b, err := protojson.Marshal(scorecardToProto(fullView()))
	require.NoError(t, err)

	for _, name := range []string{`"INTERACTION_TYPE_CALL"`, `"INTERACTION_TYPE_EMAIL"`, `"CRITERION_TYPE_SCORE"`, `"CRITERION_TYPE_OPTION"`, `"CRITERION_TYPE_YES"`} {
		assert.Contains(t, string(b), name)
	}
}

func TestScorecardFromProtoRejectsBadKeys(t *testing.T) {
	long := strings.Repeat("k", maxClientKey+1)

	for _, tt := range []struct {
		name     string
		groups   []*auditv1.CriteriaGroup
		criteria []*auditv1.Criterion
	}{
		{"group key too long", []*auditv1.CriteriaGroup{{Id: long}}, nil},
		{"criterion key too long", nil, []*auditv1.Criterion{{Id: long}}},
		{"option key too long", nil, []*auditv1.Criterion{{Options: []*auditv1.Criterion_Option{{Id: long}}}}},
		{"zero uuid as group id", []*auditv1.CriteriaGroup{{Id: uuid.Nil.String()}}, nil},
		{"zero uuid as criterion group id", nil, []*auditv1.Criterion{{GroupId: uuid.Nil.String()}}},
		{"repeated group key", []*auditv1.CriteriaGroup{{Id: "g1"}, {Id: "g1"}}, nil},
		{"criterion in an unknown group key", nil, []*auditv1.Criterion{{GroupId: "g9"}}},
		{"criterion in a key of another group list", []*auditv1.CriteriaGroup{{Id: "g1"}}, []*auditv1.Criterion{{GroupId: "g2"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := scorecardFromProto("Quality", "", false, false, nil, nil, tt.groups, tt.criteria)
			assert.Equal(t, codes.InvalidArgument, errors.Code(err))
		})
	}
}

// A client may name new rows with any short key: the server issues the uuids, and the criteria of the same request
// reach their new group through the key. uuids and empty ids keep their meaning.
func TestScorecardFromProtoTemporaryKeys(t *testing.T) {
	existing := uuid.New()

	got, err := scorecardFromProto("Quality", "", false, false, nil, nil,
		[]*auditv1.CriteriaGroup{{Id: "g1", Name: "New"}, {Id: existing.String(), Name: "Old"}, {Name: "No key"}},
		[]*auditv1.Criterion{
			{Id: "q1", GroupId: "g1", Type: auditv1.CriterionType_CRITERION_TYPE_OPTION, Options: []*auditv1.Criterion_Option{{Id: "o1"}}},
			{GroupId: existing.String()},
			{Id: "q3"},
		})
	require.NoError(t, err)

	require.Len(t, got.Groups, 3)
	assert.NotEqual(t, uuid.Nil, got.Groups[0].ID, "the key got a uuid")
	assert.Equal(t, existing, got.Groups[1].ID, "a uuid is kept")
	assert.Equal(t, uuid.Nil, got.Groups[2].ID, "empty stays for Normalize")

	require.Len(t, got.Criteria, 3)
	assert.Equal(t, got.Groups[0].ID, got.Criteria[0].GroupID, "the criterion follows the key of its group")
	assert.Equal(t, uuid.Nil, got.Criteria[0].ID, "a criterion key is not an id: Normalize issues one")
	assert.Equal(t, uuid.Nil, got.Criteria[0].Options[0].ID)
	assert.Equal(t, existing, got.Criteria[1].GroupID)
	assert.Equal(t, uuid.Nil, got.Criteria[2].GroupID)

	got.Normalize()
	assert.NotEqual(t, uuid.Nil, got.Groups[2].ID)
	assert.NotEqual(t, uuid.Nil, got.Criteria[0].ID)
	assert.NotEqual(t, uuid.Nil, got.Criteria[0].Options[0].ID)
	assert.Equal(t, got.Groups[0].ID, got.Criteria[0].GroupID, "Normalize keeps the link")
}

func TestToInt16Clamps(t *testing.T) {
	assert.Equal(t, int16(math.MaxInt16), toInt16(70_000))
	assert.Equal(t, int16(math.MinInt16), toInt16(-70_000))
	assert.Equal(t, int16(-15), toInt16(-15))
}

func TestSearchFromProto(t *testing.T) {
	no := false
	got, err := searchFromProto(&auditv1.SearchScorecardsRequest{
		Page: 2, Size: 30, Q: "q", Sort: "-name", Fields: []string{"id", "criteria_groups"}, Id: []int32{1, 2},
		TeamId: []int64{5}, Enabled: &no, Archive: &no, Editable: &no, CriterionText: "greet", TeamFilter: 9,
		InteractionType: []auditv1.InteractionType{auditv1.InteractionType_INTERACTION_TYPE_CHAT},
	})
	require.NoError(t, err)

	assert.Equal(t, &store.ScorecardSearch{
		Page: 2, Size: 30, Q: "q", Sort: "-name", Fields: []string{"id", "groups"}, IDs: []int32{1, 2},
		TeamIDs: []int64{5}, Enabled: &no, Archive: &no, Editable: &no, Question: "greet",
		TeamFilter:       true, // a flag: the number 9 is never used as a user id
		InteractionTypes: []model.InteractionType{model.InteractionTypeChat},
	}, got, "an explicit false filters")

	// No fields: the store default, the light list without groups and criteria.
	empty, err := searchFromProto(&auditv1.SearchScorecardsRequest{})
	require.NoError(t, err)
	assert.Nil(t, empty.Fields)
	assert.Nil(t, empty.Enabled, "unset does not filter")
	assert.Nil(t, empty.Editable)
	assert.Nil(t, empty.Archive)
	assert.False(t, empty.TeamFilter)
}

func TestSearchFieldsTakeProtoAndJSONNames(t *testing.T) {
	got, err := searchFromProto(&auditv1.SearchScorecardsRequest{Fields: []string{
		"id,name,criteria", "criteria_groups criteriaGroups",
		"createdAt,created_by", "updatedBy,updated_at", "interactionType interaction_type",
	}})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"id", "name", "criteria", "groups", "groups",
		"created_at", "created_by", "updated_by", "updated_at", "interaction_type", "interaction_type",
	}, got.Fields)

	// Store names and other cases are not Scorecard fields.
	for _, name := range []string{"groups", "CreatedAt", "nope"} {
		_, err := searchFromProto(&auditv1.SearchScorecardsRequest{Fields: []string{"id," + name}})
		require.Error(t, err, name)
		assert.Equal(t, codes.InvalidArgument, errors.Code(err), name)
	}
}

func TestCommentThresholdOnlyWhenSet(t *testing.T) {
	unset, err := criterionFromProto(&auditv1.Criterion{Type: auditv1.CriterionType_CRITERION_TYPE_SCORE, RequireComment: true}, nil)
	require.NoError(t, err)
	assert.Nil(t, unset.CommentThreshold, "unset stays nil, so Validate asks for it")

	zero, err := criterionFromProto(&auditv1.Criterion{CommentThreshold: new(int32(0))}, nil)
	require.NoError(t, err)
	assert.Equal(t, new(int16(0)), zero.CommentThreshold, "0 is a real threshold")

	assert.Nil(t, criterionToProto(&model.Criterion{}).CommentThreshold)
}

func TestPatchMask(t *testing.T) {
	got, err := patchMaskFromProto([]string{
		"name", "description", "enabled", "archive", "interactionType", "teams", "criteriaGroups", "criteria.0.text",
		"criteria_groups", "",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{
		service.MaskName, service.MaskDescription, service.MaskEnabled, service.MaskArchive, service.MaskInteractionTypes,
		service.MaskTeams, service.MaskGroups, service.MaskCriteria,
	}, got)

	for _, bad := range [][]string{{"secret"}, {"id"}, {"x_json_mask"}, {"name", "editable"}, nil, {""}} {
		_, err := patchMaskFromProto(bad)
		assert.Equal(t, codes.InvalidArgument, errors.Code(err), bad)
	}
}

// The service does not check the mask: the handler must refuse an unknown or empty one before the store.
func TestPatchScorecardUnknownMaskNeverReachesStore(t *testing.T) {
	// The mock has no expectations: any store call fails the test.
	srv := NewScorecardServer(service.NewScorecardService(mocks.NewMockScorecardStore(gomock.NewController(t))))
	ctx := auth.ContextWithSession(context.Background(), &auth.Session{DomainID: 1, UserID: 2})

	for _, mask := range [][]string{{"createdBy"}, nil} {
		_, err := srv.PatchScorecard(ctx, &auditv1.PatchScorecardRequest{Id: 1, XJsonMask: mask})
		assert.Equal(t, codes.InvalidArgument, errors.Code(err), mask)
	}
}

// The frontend list call: fields=id&fields=editable&fields=id&...&sort=&page=1&size=10
// (id twice, no teams or criteria).
func TestSearchScorecardsFrontendFields(t *testing.T) {
	m := mocks.NewMockScorecardStore(gomock.NewController(t))
	srv := NewScorecardServer(service.NewScorecardService(m))
	ctx := auth.ContextWithSession(context.Background(), &auth.Session{DomainID: 1, UserID: 2})

	fields := []string{
		"id", "editable", "id", "name", "description", "created_at", "created_by", "updated_at", "updated_by", "enabled",
	}

	m.EXPECT().Search(ctx, int64(1), &store.ScorecardSearch{Page: 1, Size: 10, Fields: fields}, gomock.Any()).
		Return([]*store.ScorecardView{{Scorecard: model.Scorecard{ID: 4, Name: "Quality", Editable: true}}}, false, nil)

	got, err := srv.SearchScorecards(ctx, &auditv1.SearchScorecardsRequest{Page: 1, Size: 10, Sort: "", Fields: fields})
	require.NoError(t, err)
	require.Len(t, got.GetItems(), 1)
	assert.Equal(t, "Quality", got.GetItems()[0].GetName())
	assert.Empty(t, got.GetItems()[0].GetCriteria())
}

// Unknown enum numbers are rejected by the proto rules (the server runs protovalidate before the handler),
// so the int32 -> int16 conversions never see them.
func TestUnknownEnumValuesRejected(t *testing.T) {
	v, err := protovalidate.New()
	require.NoError(t, err)

	valid := func() *auditv1.CreateScorecardRequest {
		return &auditv1.CreateScorecardRequest{
			Name:            "Quality",
			InteractionType: []auditv1.InteractionType{auditv1.InteractionType_INTERACTION_TYPE_CALL},
			Criteria:        []*auditv1.Criterion{{Type: auditv1.CriterionType_CRITERION_TYPE_SCORE, Text: "Greeting"}},
		}
	}

	require.NoError(t, v.Validate(valid()))

	badType := valid()
	badType.InteractionType = append(badType.InteractionType, auditv1.InteractionType(65537))
	require.Error(t, v.Validate(badType), "interaction type")

	// A search filter by UNSPECIFIED would silently match nothing.
	require.Error(t, v.Validate(&auditv1.SearchScorecardsRequest{
		InteractionType: []auditv1.InteractionType{auditv1.InteractionType_INTERACTION_TYPE_UNSPECIFIED},
	}), "search by unspecified interaction type")
	require.NoError(t, v.Validate(&auditv1.SearchScorecardsRequest{
		InteractionType: []auditv1.InteractionType{auditv1.InteractionType_INTERACTION_TYPE_CHAT},
	}))

	badQuestion := valid()
	badQuestion.Criteria[0].Type = auditv1.CriterionType(65539)
	require.Error(t, v.Validate(badQuestion), "question type")

	for name, msg := range map[string]proto.Message{
		"update": &auditv1.UpdateScorecardRequest{Id: 1, Name: "Quality", InteractionType: []auditv1.InteractionType{9}},
		"patch":  &auditv1.PatchScorecardRequest{Id: 1, InteractionType: []auditv1.InteractionType{9}},
		"search": &auditv1.SearchScorecardsRequest{InteractionType: []auditv1.InteractionType{9}},
	} {
		require.Error(t, v.Validate(msg), name)
	}
}

// uuid.Parse also reads 32 hex digits, braces and urn: forms; only the canonical text is an id, the rest is a key.
func TestClientIDTakesOnlyCanonicalUUIDs(t *testing.T) {
	id := uuid.New()

	got, temp, err := clientID("f", id.String())
	require.NoError(t, err)
	assert.Equal(t, id, got)
	assert.False(t, temp)

	for _, key := range []string{
		strings.ReplaceAll(id.String(), "-", ""), "{" + id.String() + "}", "urn:uuid:" + id.String(), "g1",
	} {
		got, temp, err := clientID("f", key)
		require.NoError(t, err, key)
		assert.Equal(t, uuid.Nil, got, key)
		assert.True(t, temp, key)
	}
}

// An enum number outside int16 must not wrap into a valid value (65537 would become 1, the call type): it is clamped
// and model validation refuses it.
func TestEnumNumbersDoNotWrapAround(t *testing.T) {
	got, err := scorecardFromProto("Quality", "", false, false,
		[]auditv1.InteractionType{auditv1.InteractionType(65537)}, nil, nil,
		[]*auditv1.Criterion{{Type: auditv1.CriterionType(65539)}})
	require.NoError(t, err)

	assert.Equal(t, []model.InteractionType{math.MaxInt16}, got.InteractionTypes)
	assert.Equal(t, model.CriterionType(math.MaxInt16), got.Criteria[0].Type)

	got.Normalize()
	require.Error(t, got.Validate())
}
