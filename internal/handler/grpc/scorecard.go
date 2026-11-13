package grpc

import (
	"context"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/webitel/webitel-go-kit/pkg/errors"

	auditv1 "github.com/webitel/webitel-audit/gen/go/audit/v1"
	"github.com/webitel/webitel-audit/internal/model"
	"github.com/webitel/webitel-audit/internal/service"
	"github.com/webitel/webitel-audit/internal/store"
)

// patchMask maps the proto field names of PatchScorecardRequest to the service mask names.
var patchMask = map[protoreflect.Name]string{
	"name":             service.MaskName,
	"description":      service.MaskDescription,
	"enabled":          service.MaskEnabled,
	"archive":          service.MaskArchive,
	"interaction_type": service.MaskInteractionTypes,
	"teams":            service.MaskTeams,
	"criteria_groups":  service.MaskGroups,
	"criteria":         service.MaskCriteria,
}

// fieldNames maps the Scorecard proto names whose store name differs; the rest are the same.
var fieldNames = map[protoreflect.Name]string{
	"criteria_groups": "groups",
}

// ScorecardServer implements ScorecardService.
type ScorecardServer struct {
	auditv1.UnimplementedScorecardServiceServer

	service *service.ScorecardService
}

func NewScorecardServer(s *service.ScorecardService) *ScorecardServer {
	return &ScorecardServer{service: s}
}

func (s *ScorecardServer) CreateScorecard(ctx context.Context, req *auditv1.CreateScorecardRequest) (*auditv1.CreateScorecardResponse, error) {
	in, err := scorecardFromProto(req.GetName(), req.GetDescription(), req.GetEnabled(), false,
		req.GetInteractionType(), req.GetTeams(), req.GetCriteriaGroups(), req.GetCriteria())
	if err != nil {
		return nil, err
	}

	out, err := s.service.Create(ctx, in)
	if err != nil {
		return nil, err
	}

	return &auditv1.CreateScorecardResponse{Scorecard: scorecardToProto(out)}, nil
}

func (s *ScorecardServer) ReadScorecard(ctx context.Context, req *auditv1.ReadScorecardRequest) (*auditv1.ReadScorecardResponse, error) {
	out, err := s.service.Read(ctx, req.GetId())
	if err != nil {
		return nil, err
	}

	return &auditv1.ReadScorecardResponse{Scorecard: scorecardToProto(out)}, nil
}

func (s *ScorecardServer) SearchScorecards(ctx context.Context, req *auditv1.SearchScorecardsRequest) (*auditv1.SearchScorecardsResponse, error) {
	search, err := searchFromProto(req)
	if err != nil {
		return nil, err
	}

	items, next, err := s.service.Search(ctx, search)
	if err != nil {
		return nil, err
	}

	out := &auditv1.SearchScorecardsResponse{Next: next, Items: make([]*auditv1.Scorecard, 0, len(items))}
	for _, it := range items {
		out.Items = append(out.Items, scorecardToProto(it))
	}

	return out, nil
}

func (s *ScorecardServer) UpdateScorecard(ctx context.Context, req *auditv1.UpdateScorecardRequest) (*auditv1.UpdateScorecardResponse, error) {
	in, err := scorecardFromProto(req.GetName(), req.GetDescription(), req.GetEnabled(), req.GetArchive(),
		req.GetInteractionType(), req.GetTeams(), req.GetCriteriaGroups(), req.GetCriteria())
	if err != nil {
		return nil, err
	}

	in.ID = req.GetId()

	out, err := s.service.Update(ctx, in)
	if err != nil {
		return nil, err
	}

	return &auditv1.UpdateScorecardResponse{Scorecard: scorecardToProto(out)}, nil
}

func (s *ScorecardServer) PatchScorecard(ctx context.Context, req *auditv1.PatchScorecardRequest) (*auditv1.PatchScorecardResponse, error) {
	mask, err := patchMaskFromProto(req.GetXJsonMask())
	if err != nil {
		return nil, err
	}

	in, err := scorecardFromProto(req.GetName(), req.GetDescription(), req.GetEnabled(), req.GetArchive(),
		req.GetInteractionType(), req.GetTeams(), req.GetCriteriaGroups(), req.GetCriteria())
	if err != nil {
		return nil, err
	}

	in.ID = req.GetId()

	out, err := s.service.Patch(ctx, in, mask)
	if err != nil {
		return nil, err
	}

	return &auditv1.PatchScorecardResponse{Scorecard: scorecardToProto(out)}, nil
}

func (s *ScorecardServer) DeleteScorecard(ctx context.Context, req *auditv1.DeleteScorecardRequest) (*auditv1.DeleteScorecardResponse, error) {
	out, err := s.service.Delete(ctx, req.GetId())
	if err != nil {
		return nil, err
	}

	return &auditv1.DeleteScorecardResponse{Scorecard: scorecardToProto(out)}, nil
}

// patchMaskFromProto resolves the mask paths, by JSON or proto name, to the root fields of
// PatchScorecardRequest and refuses a name that is not a patchable field.
func patchMaskFromProto(paths []string) ([]string, error) {
	fields := (&auditv1.PatchScorecardRequest{}).ProtoReflect().Descriptor().Fields()

	var mask []string

	for _, path := range paths {
		root, _, _ := strings.Cut(path, ".")
		if root == "" {
			continue
		}

		fd := fields.ByJSONName(root)
		if fd == nil {
			fd = fields.ByName(protoreflect.Name(root))
		}

		var name string
		if fd != nil {
			name = patchMask[fd.Name()]
		}

		if name == "" {
			return nil, errors.InvalidArgument("x_json_mask: unknown field "+root,
				errors.WithID("audit.scorecard.mask_unknown"), errors.WithValue("field", root))
		}

		if !slices.Contains(mask, name) {
			mask = append(mask, name)
		}
	}

	if len(mask) == 0 {
		return nil, errors.InvalidArgument("the patch names no fields", errors.WithID("audit.scorecard.mask_required"))
	}

	return mask, nil
}

// searchFromProto resolves fields, by JSON or proto name of Scorecard, to the store names
// and refuses a name that is not a Scorecard field.
func searchFromProto(req *auditv1.SearchScorecardsRequest) (*store.ScorecardSearch, error) {
	out := &store.ScorecardSearch{
		Page:     int(req.GetPage()),
		Size:     int(req.GetSize()),
		Sort:     req.GetSort(),
		Q:        req.GetQ(),
		IDs:      req.GetId(),
		TeamIDs:  req.GetTeamId(),
		Enabled:  req.Enabled,
		Editable: req.Editable,
		Archive:  req.Archive,
		Question: req.GetCriterionText(),
		// A flag only: the service takes the user from the session.
		TeamFilter: req.GetTeamFilter() != 0,
	}

	formFields := (&auditv1.Scorecard{}).ProtoReflect().Descriptor().Fields()

	for _, f := range req.GetFields() {
		for _, name := range strings.FieldsFunc(f, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
			fd := formFields.ByJSONName(name)
			if fd == nil {
				fd = formFields.ByName(protoreflect.Name(name))
			}

			if fd == nil {
				return nil, errors.InvalidArgument("fields: unknown field "+name,
					errors.WithID("audit.scorecard.field_unknown"), errors.WithValue("field", name))
			}

			storeName, ok := fieldNames[fd.Name()]
			if !ok {
				storeName = string(fd.Name())
			}

			out.Fields = append(out.Fields, storeName)
		}
	}

	for _, t := range req.GetInteractionType() {
		out.InteractionTypes = append(out.InteractionTypes, model.InteractionType(t))
	}

	return out, nil
}

func scorecardFromProto(
	name, description string, enabled, archive bool, types []auditv1.InteractionType, teams []*auditv1.Lookup,
	groups []*auditv1.CriteriaGroup, criteria []*auditv1.Criterion,
) (*model.Scorecard, error) {
	out := &model.Scorecard{Name: name, Description: description, Enabled: enabled, Archive: archive}

	for _, t := range types {
		out.InteractionTypes = append(out.InteractionTypes, model.InteractionType(t))
	}

	for _, t := range teams {
		out.TeamIDs = append(out.TeamIDs, t.GetId())
	}

	// A group id that is not a uuid is a temporary key of a new group: the server issues the uuid and the criteria
	// of the same request refer to the group by that key.
	groupKeys := make(map[string]uuid.UUID)

	for _, g := range groups {
		id, temp, err := clientID("criteria_groups.id", g.GetId())
		if err != nil {
			return nil, err
		}

		if temp {
			if _, dup := groupKeys[g.GetId()]; dup {
				return nil, errors.InvalidArgument("criteria_groups.id is repeated: "+g.GetId(),
					errors.WithID("audit.scorecard.id_duplicate"), errors.WithValue("field", "criteria_groups.id"))
			}

			id = uuid.New()
			groupKeys[g.GetId()] = id
		}

		out.Groups = append(out.Groups, model.CriteriaGroup{ID: id, Name: g.GetName(), Position: toInt16(g.GetPosition())})
	}

	for _, q := range criteria {
		c, err := criterionFromProto(q, groupKeys)
		if err != nil {
			return nil, err
		}

		out.Criteria = append(out.Criteria, c)
	}

	return out, nil
}

func criterionFromProto(q *auditv1.Criterion, groupKeys map[string]uuid.UUID) (model.Criterion, error) {
	// A key that is not a uuid marks a new row: Normalize issues its uuid, nothing else refers to it.
	id, _, err := clientID("criteria.id", q.GetId())
	if err != nil {
		return model.Criterion{}, err
	}

	groupID, temp, err := clientID("criteria.group_id", q.GetGroupId())
	if err != nil {
		return model.Criterion{}, err
	}

	if temp {
		var ok bool

		if groupID, ok = groupKeys[q.GetGroupId()]; !ok {
			return model.Criterion{}, errors.InvalidArgument("criteria.group_id "+q.GetGroupId()+" is not a group of the request",
				errors.WithID("audit.scorecard.criterion.group"), errors.WithValue("field", "criteria.group_id"))
		}
	}

	out := model.Criterion{
		ID: id, GroupID: groupID, Position: toInt16(q.GetPosition()), Type: model.CriterionType(q.GetType()),
		Question: q.GetText(), Description: q.GetDescription(), Required: q.GetRequired(),
		CriticalViolation: q.GetCriticalViolation(), AllowNA: q.GetAllowNa(),
		Min: toInt16(q.GetMin()), Max: toInt16(q.GetMax()), Weight: q.GetWeight(),
		RequireComment: q.GetRequireComment(),
	}

	// Normalize drops the threshold unless the criterion is a Score that requires a comment.
	if q.CommentThreshold != nil {
		threshold := toInt16(q.GetCommentThreshold())
		out.CommentThreshold = &threshold
	}

	for _, o := range q.GetOptions() {
		oid, _, err := clientID("criteria.options.id", o.GetId())
		if err != nil {
			return model.Criterion{}, err
		}

		out.Options = append(out.Options, model.Option{
			ID: oid, Position: toInt16(o.GetPosition()), Name: o.GetName(), Score: o.GetScore(),
			RequireComment: o.GetRequireComment(),
		})
	}

	return out, nil
}

// maxClientKey bounds a temporary key a client uses for a new row.
const maxClientKey = 64

// uuidLen is the length of the canonical uuid text; uuid.Parse also takes other spellings, which stay temporary keys.
const uuidLen = 36

// clientID reads an id a client sent: empty is uuid.Nil, a canonical uuid is an existing (or client-made) row, any other
// short string is a temporary key of a new row (temp), for the caller to resolve; Normalize issues uuids for Nil.
// The zero uuid is refused: it would read as "no id" and silently cut a criterion off its group.
func clientID(field, s string) (id uuid.UUID, temp bool, err error) {
	if s == "" {
		return uuid.Nil, false, nil
	}

	if len(s) == uuidLen {
		if id, err := uuid.Parse(s); err == nil {
			if id == uuid.Nil {
				return uuid.Nil, false, errors.InvalidArgument(field+" must not be the zero uuid",
					errors.WithID("audit.scorecard.id_invalid"), errors.WithValue("field", field))
			}

			return id, false, nil
		}
	}

	if len(s) > maxClientKey {
		return uuid.Nil, false, errors.InvalidArgument(field+" is longer than "+strconv.Itoa(maxClientKey)+" characters",
			errors.WithID("audit.scorecard.id_invalid"), errors.WithValue("field", field))
	}

	return uuid.Nil, true, nil
}

// toInt16 clamps, so an out-of-range value still fails model validation instead of wrapping around.
func toInt16(v int32) int16 {
	return int16(min(max(v, math.MinInt16), math.MaxInt16))
}

func scorecardToProto(v *store.ScorecardView) *auditv1.Scorecard {
	out := &auditv1.Scorecard{
		Id:          v.ID,
		CreatedAt:   v.CreatedAt.UnixMilli(),
		CreatedBy:   lookupToProto(v.Creator),
		UpdatedAt:   v.UpdatedAt.UnixMilli(),
		UpdatedBy:   lookupToProto(v.Updater),
		Name:        v.Name,
		Description: v.Description,
		Enabled:     v.Enabled,
		Archive:     v.Archive,
		Editable:    v.Editable,
	}

	for _, t := range v.InteractionTypes {
		out.InteractionType = append(out.InteractionType, auditv1.InteractionType(t))
	}

	for _, t := range v.Teams {
		out.Teams = append(out.Teams, lookupToProto(&t))
	}

	for _, g := range v.Groups {
		out.CriteriaGroups = append(out.CriteriaGroups, &auditv1.CriteriaGroup{
			Id: g.ID.String(), Name: g.Name, Position: int32(g.Position),
		})
	}

	for i := range v.Criteria {
		out.Criteria = append(out.Criteria, criterionToProto(&v.Criteria[i]))
	}

	return out
}

func criterionToProto(c *model.Criterion) *auditv1.Criterion {
	out := &auditv1.Criterion{
		Id: c.ID.String(), Position: int32(c.Position), Type: auditv1.CriterionType(c.Type),
		Text: c.Question, Description: c.Description, Required: c.Required,
		CriticalViolation: c.CriticalViolation, AllowNa: c.AllowNA,
		Min: int32(c.Min), Max: int32(c.Max), Weight: c.Weight, RequireComment: c.RequireComment,
	}

	if c.GroupID != uuid.Nil {
		out.GroupId = c.GroupID.String()
	}

	if c.CommentThreshold != nil {
		out.CommentThreshold = new(int32(*c.CommentThreshold))
	}

	for _, o := range c.Options {
		out.Options = append(out.Options, &auditv1.Criterion_Option{
			Id: o.ID.String(), Position: int32(o.Position), Name: o.Name, Score: o.Score,
			RequireComment: o.RequireComment,
		})
	}

	return out
}

func lookupToProto(l *store.Lookup) *auditv1.Lookup {
	if l == nil {
		return nil
	}

	return &auditv1.Lookup{Id: l.ID, Name: l.Name}
}
