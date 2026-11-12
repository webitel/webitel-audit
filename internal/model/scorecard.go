package model

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/webitel/webitel-go-kit/pkg/errors"
)

// CriterionType is the answer kind of a criterion; numbers match engine AuditQuestionType.
type CriterionType int16

const (
	CriterionTypeScore  CriterionType = 1
	CriterionTypeOption CriterionType = 2
	CriterionTypeYes    CriterionType = 3
)

type InteractionType int16

const (
	InteractionTypeCall  InteractionType = 1
	InteractionTypeChat  InteractionType = 2
	InteractionTypeEmail InteractionType = 3
)

// Scale and weight limits of a criterion.
const (
	ScoreMin  = -15
	ScoreMax  = 15
	WeightMin = 1
	WeightMax = 10
)

const (
	MaxCriteria          = 500
	MaxOptions           = 50
	MaxGroups            = 100
	MaxQuestionLen       = 2000
	MaxCriterionDescrLen = 10000
	MaxOptionNameLen     = 256 // same as a group name
)

var ErrNotEditable = errors.Forbidden("scorecard has evaluations, its structure cannot be changed",
	errors.WithID("audit.scorecard.not_editable"))

type Scorecard struct {
	ID          int32
	DomainID    int64
	Name        string
	Description string
	Enabled     bool
	Archive     bool
	// Editable is false once the form was used;
	Editable         bool
	InteractionTypes []InteractionType
	TeamIDs          []int64
	Groups           []CriteriaGroup
	// Criteria of the whole form in display order; a criterion joins a group by GroupID.
	Criteria  []Criterion
	CreatedAt time.Time
	CreatedBy int64
	UpdatedAt time.Time
	UpdatedBy int64
}

type CriteriaGroup struct {
	ID       uuid.UUID
	Name     string
	Position int16
}

// Criterion is one question of a scorecard.
type Criterion struct {
	ID uuid.UUID
	// GroupID is uuid.Nil when the criterion has no group.
	GroupID           uuid.UUID
	Position          int16
	Type              CriterionType
	Question          string
	Description       string
	Required          bool
	CriticalViolation bool
	AllowNA           bool
	// Min and Max bound the scale of a Score criterion; zero for other types.
	Min    int16
	Max    int16
	Weight float64
	// RequireComment asks the evaluator for a comment; for Score only when the score is <= CommentThreshold.
	RequireComment bool
	// CommentThreshold is set only for a Score criterion that requires a comment.
	CommentThreshold *int16
	// Options of an Option criterion; empty for other types.
	Options []Option
}

// Option is one answer of an Option criterion.
type Option struct {
	ID             uuid.UUID
	Position       int16
	Name           string
	Score          float32
	RequireComment bool
}

func (c *Criterion) MaxScore() float64 {
	switch c.Type {
	case CriterionTypeScore:
		return float64(c.Max) * c.Weight
	case CriterionTypeOption:
		if len(c.Options) == 0 {
			return 0
		}

		best := c.Options[0].Score
		for _, o := range c.Options {
			best = max(best, o.Score)
		}

		return float64(best) * c.Weight
	case CriterionTypeYes:
	}

	return 0
}

func (s *Scorecard) Clone() Scorecard {
	out := *s
	out.InteractionTypes = slices.Clone(s.InteractionTypes)
	out.TeamIDs = slices.Clone(s.TeamIDs)
	out.Groups = slices.Clone(s.Groups)
	out.Criteria = slices.Clone(s.Criteria)

	for i := range out.Criteria {
		c := &out.Criteria[i]
		c.Options = slices.Clone(c.Options)

		if c.CommentThreshold != nil {
			v := *c.CommentThreshold
			c.CommentThreshold = &v
		}
	}

	return out
}

// Normalize applies the server-side defaults before Validate: positions follow slice order
// (the client position is ignored), empty ids are generated, names are trimmed.
func (s *Scorecard) Normalize() {
	s.Name = strings.TrimSpace(s.Name)

	slices.Sort(s.InteractionTypes)

	for i := range s.Groups {
		g := &s.Groups[i]
		g.Position = int16(i)
		g.Name = strings.TrimSpace(g.Name)

		if g.ID == uuid.Nil {
			g.ID = uuid.New()
		}
	}

	for i := range s.Criteria {
		c := &s.Criteria[i]
		c.Position = int16(i)
		c.Question = strings.TrimSpace(c.Question)

		if c.ID == uuid.Nil {
			c.ID = uuid.New()
		}

		if c.Weight == 0 || c.Type == CriterionTypeYes {
			c.Weight = 1
		}

		if c.Type != CriterionTypeScore {
			c.Min, c.Max = 0, 0
		}

		if c.Type != CriterionTypeScore || !c.RequireComment {
			c.CommentThreshold = nil
		}

		if c.Type != CriterionTypeOption {
			c.Options = nil
		} else {
			c.RequireComment = false
		}

		if c.Type != CriterionTypeYes {
			c.CriticalViolation = false
		}

		if c.Type == CriterionTypeYes {
			c.Required = false
			if c.CriticalViolation {
				c.RequireComment = true
			}
		}

		for j := range c.Options {
			o := &c.Options[j]
			o.Position = int16(j)
			o.Name = strings.TrimSpace(o.Name)

			if o.ID == uuid.Nil {
				o.ID = uuid.New()
			}
		}
	}
}

// Validate checks a normalized scorecard and returns an InvalidArgument error with id
// audit.scorecard.<reason> for the first broken rule.
func (s *Scorecard) Validate() error {
	if n := utf8.RuneCountInString(s.Name); n < 3 || n > 256 {
		return invalid("name", "name must be 3 to 256 characters")
	}

	if utf8.RuneCountInString(s.Description) > 516 {
		return invalid("description", "description must be at most 516 characters")
	}

	if len(s.InteractionTypes) == 0 {
		return invalid("interaction_type", "at least one interaction type is required")
	}

	seenTypes := make(map[InteractionType]bool)

	for _, t := range s.InteractionTypes {
		if t < InteractionTypeCall || t > InteractionTypeEmail {
			return invalid("interaction_type", fmt.Sprintf("unknown interaction type %d", t))
		}

		if seenTypes[t] {
			return invalid("interaction_type", fmt.Sprintf("duplicate interaction type %d", t))
		}

		seenTypes[t] = true
	}

	if len(s.Criteria) > MaxCriteria || len(s.Groups) > MaxGroups {
		return invalid("too_many", fmt.Sprintf("a form takes at most %d criteria and %d groups", MaxCriteria, MaxGroups))
	}

	seenIDs := make(map[uuid.UUID]bool)
	groupIDs := make(map[uuid.UUID]bool)
	groupNames := make(map[string]bool)

	for _, g := range s.Groups {
		if seenIDs[g.ID] {
			return invalid("duplicate_id", "duplicate id "+g.ID.String())
		}

		seenIDs[g.ID] = true
		groupIDs[g.ID] = true

		if g.Name == "" {
			return invalid("group.name", "group name is required")
		}

		if utf8.RuneCountInString(g.Name) > 256 {
			return invalid("group.name", "group name must be at most 256 characters")
		}

		if groupNames[g.Name] {
			return invalid("group.name_duplicate", "duplicate group name "+g.Name)
		}

		groupNames[g.Name] = true
	}

	hasScoredRequired := false

	for i := range s.Criteria {
		c := &s.Criteria[i]
		if err := c.validate(seenIDs, groupIDs); err != nil {
			return errors.Wrap(err, errors.PrependMessage(fmt.Sprintf("criterion %d", i+1)))
		}

		hasScoredRequired = hasScoredRequired || (c.Required && c.MaxScore() > 0)
	}

	if s.Enabled && !hasScoredRequired {
		return invalid("no_required_criterion", "Add at least one required criterion with a maximum score above 0")
	}

	return nil
}

// validate checks one criterion; seenIDs holds every id met so far, groupIDs only the ids of the form's groups:
// a criterion may sit only in one of them (not under the id of another criterion or a group of another form).
func (c *Criterion) validate(seenIDs, groupIDs map[uuid.UUID]bool) error {
	if seenIDs[c.ID] {
		return invalid("duplicate_id", "duplicate id "+c.ID.String())
	}

	if c.GroupID != uuid.Nil && !groupIDs[c.GroupID] {
		return invalid("criterion.group", "unknown group "+c.GroupID.String())
	}

	seenIDs[c.ID] = true

	if c.Question == "" {
		return invalid("criterion.question", "question is required")
	}

	if utf8.RuneCountInString(c.Question) > MaxQuestionLen {
		return invalid("criterion.question", fmt.Sprintf("question must be at most %d characters", MaxQuestionLen))
	}

	if utf8.RuneCountInString(c.Description) > MaxCriterionDescrLen {
		return invalid("criterion.description", fmt.Sprintf("description must be at most %d characters", MaxCriterionDescrLen))
	}

	if math.IsNaN(c.Weight) || math.IsInf(c.Weight, 0) {
		return invalid("criterion.weight", "weight must be a finite number")
	}

	if c.Weight < WeightMin || c.Weight > WeightMax || math.Abs(c.Weight*100-math.Round(c.Weight*100)) > 1e-9 {
		return invalid("criterion.weight", "weight must be 1 to 10 with at most two decimals")
	}

	switch c.Type {
	case CriterionTypeScore:
		if c.Min < ScoreMin || c.Max > ScoreMax || c.Min > c.Max || c.Max < 0 {
			return invalid("criterion.score_range", "score range must satisfy -15 <= min <= max <= 15 and max >= 0")
		}

		if c.RequireComment && c.CommentThreshold == nil {
			return invalid("criterion.comment_threshold", "comment threshold is required")
		}

		if t := c.CommentThreshold; t != nil && (*t < c.Min || *t > c.Max) {
			return invalid("criterion.comment_threshold", "comment threshold must be within the score range")
		}
	case CriterionTypeOption:
		if len(c.Options) == 0 {
			return invalid("criterion.options", "at least one option is required")
		}

		if len(c.Options) > MaxOptions {
			return invalid("criterion.options", fmt.Sprintf("a criterion takes at most %d options", MaxOptions))
		}

		hasNonNegative := false

		for _, o := range c.Options {
			if seenIDs[o.ID] {
				return invalid("duplicate_id", "duplicate id "+o.ID.String())
			}

			seenIDs[o.ID] = true

			if o.Name == "" {
				return invalid("criterion.option_name", "option name is required")
			}

			if utf8.RuneCountInString(o.Name) > MaxOptionNameLen {
				return invalid("criterion.option_name", fmt.Sprintf("option name must be at most %d characters", MaxOptionNameLen))
			}

			if math.IsNaN(float64(o.Score)) || o.Score < ScoreMin || o.Score > ScoreMax {
				return invalid("criterion.option_score", "option score must be within -15..15")
			}

			hasNonNegative = hasNonNegative || o.Score >= 0
		}

		if !hasNonNegative {
			return invalid("criterion.option_score", "at least one option score must be 0 or more")
		}
	case CriterionTypeYes:
	default:
		return invalid("criterion.type", fmt.Sprintf("unknown criterion type %d", c.Type))
	}

	return nil
}

func invalid(reason, msg string) error {
	return errors.InvalidArgument(msg, errors.WithID("audit.scorecard."+reason))
}

type snapshotContent struct {
	Groups   []snapshotGroup     `json:"groups"`
	Criteria []snapshotCriterion `json:"criteria"`
}

type snapshotGroup struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type snapshotCriterion struct {
	ID                uuid.UUID        `json:"id"`
	GroupID           uuid.UUID        `json:"group_id"`
	Type              CriterionType    `json:"type"`
	Question          string           `json:"question"`
	Description       string           `json:"description"`
	Required          bool             `json:"required"`
	CriticalViolation bool             `json:"critical_violation"`
	AllowNA           bool             `json:"allow_na"`
	Min               int16            `json:"min"`
	Max               int16            `json:"max"`
	Weight            float64          `json:"weight"`
	RequireComment    bool             `json:"require_comment"`
	CommentThreshold  *int16           `json:"comment_threshold"`
	Options           []snapshotOption `json:"options"`
}

type snapshotOption struct {
	ID             uuid.UUID `json:"id"`
	Name           string    `json:"name"`
	Score          float32   `json:"score"`
	RequireComment bool      `json:"require_comment"`
}

func (s *Scorecard) SnapshotContent() []byte {
	return s.content(true)
}

// ContentHash is the sha256 of SnapshotContent, stored in audit.scorecard_snapshot.hash.
func (s *Scorecard) ContentHash() []byte {
	sum := sha256.Sum256(s.SnapshotContent())

	return sum[:]
}

func (s *Scorecard) StructureEqual(next *Scorecard) bool {
	if !slices.Equal(slices.Sorted(slices.Values(s.InteractionTypes)), slices.Sorted(slices.Values(next.InteractionTypes))) {
		return false
	}

	return string(s.content(false)) == string(next.content(false))
}

// content is the canonical JSON of the form in position order; full also adds the descriptive texts (group names, criterion descriptions) that StructureEqual ignores.
func (s *Scorecard) content(full bool) []byte {
	out := snapshotContent{
		Groups:   make([]snapshotGroup, 0, len(s.Groups)),
		Criteria: make([]snapshotCriterion, 0, len(s.Criteria)),
	}

	for _, g := range s.Groups {
		sg := snapshotGroup{ID: g.ID}
		if full {
			sg.Name = g.Name
		}

		out.Groups = append(out.Groups, sg)
	}

	for _, c := range s.Criteria {
		sc := snapshotCriterion{
			ID: c.ID, GroupID: c.GroupID, Type: c.Type, Question: c.Question,
			Required: c.Required, CriticalViolation: c.CriticalViolation, AllowNA: c.AllowNA,
			Min: c.Min, Max: c.Max, Weight: c.Weight,
			RequireComment: c.RequireComment, CommentThreshold: c.CommentThreshold,
			Options: make([]snapshotOption, 0, len(c.Options)),
		}
		if full {
			sc.Description = c.Description
		}

		for _, o := range c.Options {
			sc.Options = append(sc.Options, snapshotOption{ID: o.ID, Name: o.Name, Score: o.Score, RequireComment: o.RequireComment})
		}

		out.Criteria = append(out.Criteria, sc)
	}

	// Marshal cannot fail: the structs hold only strings, numbers, bools and uuids.
	b, _ := json.Marshal(out)

	return b
}
