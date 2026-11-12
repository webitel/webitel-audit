package model

import (
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/webitel/webitel-go-kit/pkg/errors"
)

func ptr[T any](v T) *T { return &v }

// validScorecard is an enabled form that passes Validate after Normalize.
func validScorecard() *Scorecard {
	return &Scorecard{
		Name:             "Call quality",
		Enabled:          true,
		InteractionTypes: []InteractionType{InteractionTypeCall},
		TeamIDs:          []int64{1},
		Groups:           []CriteriaGroup{{Name: "Greeting"}},
		Criteria: []Criterion{
			{Type: CriterionTypeScore, Question: "Greeted the customer", Required: true, Min: -2, Max: 5},
			{Type: CriterionTypeOption, Question: "Tone", Options: []Option{{Name: "Bad", Score: -1}, {Name: "Good", Score: 2.5}}},
			{Type: CriterionTypeYes, Question: "Rude words", CriticalViolation: true},
		},
	}
}

func TestScorecardNormalize(t *testing.T) {
	s := validScorecard()
	s.Name = "  Call quality  "
	s.Groups[0].Name = " Greeting "
	s.Criteria[0].Position = 7
	s.Criteria[2].Required = true
	s.Criteria[1].Min, s.Criteria[1].Max = 1, 3
	s.Criteria[0].Options = []Option{{Name: "stray"}}
	s.Normalize()

	assert.Equal(t, "Call quality", s.Name)
	assert.Equal(t, "Greeting", s.Groups[0].Name)
	assert.NotEqual(t, uuid.Nil, s.Groups[0].ID)

	for i, c := range s.Criteria {
		assert.Equal(t, int16(i), c.Position, "position follows slice order, the client value is ignored")
		assert.NotEqual(t, uuid.Nil, c.ID)
		assert.Equal(t, 1.0, c.Weight, "weight 0 defaults to 1")
	}

	assert.Equal(t, int16(1), s.Criteria[1].Options[1].Position)
	assert.NotEqual(t, uuid.Nil, s.Criteria[1].Options[0].ID)
	assert.Nil(t, s.Criteria[0].Options, "options of a non-Option criterion are dropped")
	assert.Zero(t, s.Criteria[1].Max, "scale of a non-Score criterion is dropped")
	assert.False(t, s.Criteria[2].Required)
	assert.True(t, s.Criteria[2].RequireComment)
}

func TestScorecardNormalizeDropsOtherTypeFlags(t *testing.T) {
	s := validScorecard()
	s.Criteria[1].Weight = 3
	s.Criteria[1].RequireComment, s.Criteria[1].CommentThreshold = true, ptr[int16](1)
	s.Criteria[2].Weight = 5
	s.Normalize()

	assert.InDelta(t, 3.0, s.Criteria[1].Weight, 0, "Option keeps its weight")
	assert.InDelta(t, 1.0, s.Criteria[2].Weight, 0, `Say "Yes" weight is reset to 1`)
	assert.False(t, s.Criteria[1].RequireComment)
	assert.Nil(t, s.Criteria[1].CommentThreshold)
}

func TestScorecardNormalizeKeepsCriticalViolationOnYesOnly(t *testing.T) {
	s := validScorecard()
	s.Criteria[0].CriticalViolation = true
	s.Criteria[1].CriticalViolation = true
	s.Normalize()

	assert.False(t, s.Criteria[0].CriticalViolation, "Score has no critical violation")
	assert.False(t, s.Criteria[1].CriticalViolation, "Option has no critical violation")
	assert.True(t, s.Criteria[2].CriticalViolation, `Say "Yes" keeps it`)
}

func TestScorecardValidate(t *testing.T) {
	tests := []struct {
		name   string
		modify func(s *Scorecard)
		wantID string // empty: valid
	}{
		{"valid", func(*Scorecard) {}, ""},
		// Engine AuditForm.IsValid: name 3..256 characters, description up to 516.
		{"name too short", func(s *Scorecard) { s.Name = "ab" }, "audit.scorecard.name"},
		{"name counted in characters", func(s *Scorecard) { s.Name = strings.Repeat("ї", 256) }, ""},
		{"name too long", func(s *Scorecard) { s.Name = strings.Repeat("a", 257) }, "audit.scorecard.name"},
		{"description too long", func(s *Scorecard) { s.Description = strings.Repeat("a", 517) }, "audit.scorecard.description"},
		{"no interaction type", func(s *Scorecard) { s.InteractionTypes = nil }, "audit.scorecard.interaction_type"},
		{"unknown interaction type", func(s *Scorecard) { s.InteractionTypes = []InteractionType{4} }, "audit.scorecard.interaction_type"},
		{"duplicate interaction type", func(s *Scorecard) {
			s.InteractionTypes = []InteractionType{InteractionTypeChat, InteractionTypeChat}
		}, "audit.scorecard.interaction_type"},
		{"unknown criterion type", func(s *Scorecard) { s.Criteria[0].Type = 4 }, "audit.scorecard.criterion.type"},
		{"empty question", func(s *Scorecard) { s.Criteria[0].Question = "  " }, "audit.scorecard.criterion.question"},
		{"score min below -15", func(s *Scorecard) { s.Criteria[0].Min = -16 }, "audit.scorecard.criterion.score_range"},
		{"score max above 15", func(s *Scorecard) { s.Criteria[0].Max = 16 }, "audit.scorecard.criterion.score_range"},
		{"score min above max", func(s *Scorecard) { s.Criteria[0].Min = 6 }, "audit.scorecard.criterion.score_range"},
		{"score min equals max", func(s *Scorecard) { s.Criteria[0].Min = 5 }, ""},
		{"score max negative", func(s *Scorecard) { s.Criteria[0].Min, s.Criteria[0].Max = -5, -1 }, "audit.scorecard.criterion.score_range"},
		{"option without options", func(s *Scorecard) { s.Criteria[1].Options = nil }, "audit.scorecard.criterion.options"},
		{"option score out of range", func(s *Scorecard) { s.Criteria[1].Options[0].Score = -15.5 }, "audit.scorecard.criterion.option_score"},
		{"option scores all negative", func(s *Scorecard) { s.Criteria[1].Options[1].Score = -0.5 }, "audit.scorecard.criterion.option_score"},
		{"weight NaN", func(s *Scorecard) { s.Criteria[0].Weight = math.NaN() }, "audit.scorecard.criterion.weight"},
		{"weight +Inf", func(s *Scorecard) { s.Criteria[0].Weight = math.Inf(1) }, "audit.scorecard.criterion.weight"},
		{"weight -Inf", func(s *Scorecard) { s.Criteria[0].Weight = math.Inf(-1) }, "audit.scorecard.criterion.weight"},
		{"weight below 1", func(s *Scorecard) { s.Criteria[0].Weight = 0.5 }, "audit.scorecard.criterion.weight"},
		{"weight above 10", func(s *Scorecard) { s.Criteria[0].Weight = 10.01 }, "audit.scorecard.criterion.weight"},
		{"weight three decimals", func(s *Scorecard) { s.Criteria[0].Weight = 1.125 }, "audit.scorecard.criterion.weight"},
		{"weight two decimals", func(s *Scorecard) { s.Criteria[0].Weight = 9.99 }, ""},
		{"comment threshold missing", func(s *Scorecard) { s.Criteria[0].RequireComment = true }, "audit.scorecard.criterion.comment_threshold"},
		{"comment threshold outside scale", func(s *Scorecard) {
			s.Criteria[0].RequireComment, s.Criteria[0].CommentThreshold = true, ptr[int16](6)
		}, "audit.scorecard.criterion.comment_threshold"},
		{"comment threshold inside scale", func(s *Scorecard) {
			s.Criteria[0].RequireComment, s.Criteria[0].CommentThreshold = true, ptr[int16](-2)
		}, ""},
		{"empty group name", func(s *Scorecard) { s.Groups[0].Name = "   " }, "audit.scorecard.group.name"},
		{"duplicate group name", func(s *Scorecard) {
			s.Groups = append(s.Groups, CriteriaGroup{Name: "Greeting "})
		}, "audit.scorecard.group.name_duplicate"},
		{"group names differ in case", func(s *Scorecard) {
			s.Groups = append(s.Groups, CriteriaGroup{Name: "GREETING"})
		}, ""},
		// Limits of the model, no spec rule: see MaxCriteria and the other constants.
		{"too many criteria", func(s *Scorecard) {
			for range MaxCriteria {
				s.Criteria = append(s.Criteria, Criterion{Type: CriterionTypeYes, Question: "q"})
			}
		}, "audit.scorecard.too_many"},
		{"too many groups", func(s *Scorecard) {
			for i := range MaxGroups {
				s.Groups = append(s.Groups, CriteriaGroup{Name: strings.Repeat("g", i+2)})
			}
		}, "audit.scorecard.too_many"},
		{"too many options", func(s *Scorecard) {
			for range MaxOptions {
				s.Criteria[1].Options = append(s.Criteria[1].Options, Option{Name: "o", Score: 1})
			}
		}, "audit.scorecard.criterion.options"},
		{"question too long", func(s *Scorecard) { s.Criteria[0].Question = strings.Repeat("q", MaxQuestionLen+1) }, "audit.scorecard.criterion.question"},
		{"question at the limit", func(s *Scorecard) { s.Criteria[0].Question = strings.Repeat("ї", MaxQuestionLen) }, ""},
		{"criterion description too long", func(s *Scorecard) {
			s.Criteria[0].Description = strings.Repeat("d", MaxCriterionDescrLen+1)
		}, "audit.scorecard.criterion.description"},
		{"empty option name", func(s *Scorecard) { s.Criteria[1].Options[0].Name = " " }, "audit.scorecard.criterion.option_name"},
		{"option name too long", func(s *Scorecard) {
			s.Criteria[1].Options[0].Name = strings.Repeat("o", MaxOptionNameLen+1)
		}, "audit.scorecard.criterion.option_name"},
		{"option name at the limit", func(s *Scorecard) { s.Criteria[1].Options[0].Name = strings.Repeat("ї", MaxOptionNameLen) }, ""},
		{"option score NaN", func(s *Scorecard) { s.Criteria[1].Options[0].Score = float32(math.NaN()) }, "audit.scorecard.criterion.option_score"},
		{"option score Inf", func(s *Scorecard) { s.Criteria[1].Options[0].Score = float32(math.Inf(1)) }, "audit.scorecard.criterion.option_score"},
		{"criterion in unknown group", func(s *Scorecard) { s.Criteria[0].GroupID = uuid.New() }, "audit.scorecard.criterion.group"},
		// A group_id equal to the id of an EARLIER criterion is not a group either (a check against the ids
		// seen so far, instead of the group ids, would let it pass).
		{"criterion in a criterion id", func(s *Scorecard) { s.Criteria[0].ID = uuid.New(); s.Criteria[1].GroupID = s.Criteria[0].ID }, "audit.scorecard.criterion.group"},
		{"duplicate id", func(s *Scorecard) { s.Criteria[1].ID = s.Criteria[0].ID }, "audit.scorecard.duplicate_id"},
		{"enabled without required scored criterion", func(s *Scorecard) { s.Criteria[0].Required = false }, "audit.scorecard.no_required_criterion"},
		{"enabled with required zero-max criterion", func(s *Scorecard) { s.Criteria[0].Min, s.Criteria[0].Max = -3, 0 }, "audit.scorecard.no_required_criterion"},
		{"enabled with required option criterion", func(s *Scorecard) {
			s.Criteria[0].Required, s.Criteria[1].Required = false, true
		}, ""},
		{"disabled empty form", func(s *Scorecard) { s.Enabled, s.Groups, s.Criteria = false, nil, nil }, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validScorecard()
			s.Criteria[0].ID = uuid.New() // fixed before Normalize so "duplicate id" can copy it
			tt.modify(s)
			s.Normalize()

			err := s.Validate()
			if tt.wantID == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)

			id, _ := errors.Lookup(err, errors.ErrKeyID)
			assert.Equal(t, tt.wantID, id)

			code, _ := errors.Lookup(err, errors.ErrKeyCode)
			assert.Equal(t, codes.InvalidArgument, code)
		})
	}
}

func TestCriterionMaxScore(t *testing.T) {
	option := Criterion{Type: CriterionTypeOption, Weight: 2, Options: []Option{{Score: -3}, {Score: 4}, {Score: 1}}}
	assert.InDelta(t, 8.0, option.MaxScore(), 1e-9)

	score := Criterion{Type: CriterionTypeScore, Weight: 1.5, Min: -1, Max: 10}
	assert.InDelta(t, 15.0, score.MaxScore(), 1e-9)

	yes := Criterion{Type: CriterionTypeYes, Weight: 3}
	assert.Zero(t, yes.MaxScore())
}

func TestScorecardContentHash(t *testing.T) {
	base := validScorecard()
	base.Normalize()
	hash := base.ContentHash()

	clone := func() *Scorecard {
		c := *base
		c.Criteria = append([]Criterion(nil), base.Criteria...)

		return &c
	}

	same := clone()
	assert.Equal(t, hash, same.ContentHash(), "same content, same hash")
	assert.JSONEq(t, string(base.SnapshotContent()), string(same.SnapshotContent()))

	reordered := clone()
	reordered.Criteria[0], reordered.Criteria[1] = reordered.Criteria[1], reordered.Criteria[0]
	reordered.Normalize()
	assert.NotEqual(t, hash, reordered.ContentHash(), "criteria order is content")

	descriptive := clone()
	descriptive.Name, descriptive.Description = "Other name", "other"
	descriptive.TeamIDs = []int64{2, 3}
	descriptive.Enabled, descriptive.Archive, descriptive.Editable = false, true, true
	assert.Equal(t, hash, descriptive.ContentHash(), "name, description, teams and flags are not content")

	weighted := clone()
	weighted.Criteria[0].Weight = 2
	assert.NotEqual(t, hash, weighted.ContentHash())
}

func TestScorecardStructureEqual(t *testing.T) {
	base := validScorecard()
	base.InteractionTypes = []InteractionType{InteractionTypeCall, InteractionTypeChat}
	base.Normalize()

	clone := func() *Scorecard {
		next := *base
		next.Groups = append([]CriteriaGroup(nil), base.Groups...)
		next.Criteria = append([]Criterion(nil), base.Criteria...)

		return &next
	}

	next := clone()
	next.Name, next.Description, next.TeamIDs, next.Archive = "Renamed", "new", nil, true
	next.Criteria[0].Description = "clarified"
	next.Groups[0].Name = "Opening"
	next.InteractionTypes = []InteractionType{InteractionTypeChat, InteractionTypeCall}
	assert.True(t, base.StructureEqual(next), "descriptive edits and another type order keep the structure")
	assert.NotEqual(t, base.ContentHash(), next.ContentHash(), "criterion description and group name are still snapshot content")

	for name, change := range map[string]func(*Scorecard){
		"scale":            func(s *Scorecard) { s.Criteria[0].Max = 4 },
		"interaction type": func(s *Scorecard) { s.InteractionTypes = []InteractionType{InteractionTypeCall} },
		"group membership": func(s *Scorecard) { s.Criteria[1].GroupID = s.Groups[0].ID },
		"group added":      func(s *Scorecard) { s.Groups = append(s.Groups, CriteriaGroup{ID: uuid.New(), Name: "x", Position: 1}) },
	} {
		next := clone()
		change(next)
		assert.False(t, base.StructureEqual(next), name)
	}
}

func TestScorecardNormalizeSortsInteractionTypes(t *testing.T) {
	s := &Scorecard{InteractionTypes: []InteractionType{InteractionTypeEmail, InteractionTypeCall}}
	s.Normalize()

	assert.Equal(t, []InteractionType{InteractionTypeCall, InteractionTypeEmail}, s.InteractionTypes)
}
