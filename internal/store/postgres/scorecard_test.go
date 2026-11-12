package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/webitel/webitel-go-kit/pkg/errors"

	"github.com/webitel/webitel-audit/internal/model"
	"github.com/webitel/webitel-audit/internal/store"
	"github.com/webitel/webitel-audit/internal/store/util"
)

func TestBuildSearchDefaults(t *testing.T) {
	sql, args, err := buildSearch(7, &store.ScorecardSearch{}, nil, util.NewPaging(10, 2))
	require.NoError(t, err)

	assert.Contains(t, sql, "WHERE s.domain_id = $1")
	assert.Contains(t, sql, "ORDER BY s.created_at DESC, s.id")
	assert.Contains(t, sql, "LIMIT 11 OFFSET 10")
	assert.NotContains(t, sql, "scorecard_acl")
	assert.Equal(t, []any{int64(7)}, args)
}

func TestBuildSearchFilters(t *testing.T) {
	yes := true
	sql, args, err := buildSearch(1, &store.ScorecardSearch{
		Q: "50%_off*", IDs: []int32{3}, TeamIDs: []int64{4}, Enabled: &yes, Editable: &yes, Archive: &yes,
		InteractionTypes: []model.InteractionType{model.InteractionTypeChat}, Question: "*greet", Sort: "+name",
	}, &store.RBAC{Roles: []int64{9}}, util.NewPaging(5, 1))
	require.NoError(t, err)

	for _, part := range []string{
		"a.subject = ANY($2::bigint[]) AND a.access & $3::int = $4::int",
		"(s.name ILIKE $5 OR s.description ILIKE $6)",
		"s.id = ANY($7::int[])",
		"st.team_id = ANY($8::bigint[])",
		"s.enabled = $9", "s.editable = $10", "s.archive = $11",
		"s.interaction_type && $12::smallint[]",
		"c.question ILIKE $13",
		"ORDER BY s.name ASC, s.id",
	} {
		assert.Contains(t, sql, part)
	}

	assert.Equal(t, store.AccessRead, args[2], "search checks the read bit")
	// Engine ReplaceWebSearch: only `*` is a wildcard and nothing is added around the text.
	assert.Equal(t, `50\%\_off%`, args[4])
	assert.Equal(t, "%greet", args[12])
}

func TestBuildSearchTeamFilter(t *testing.T) {
	sql, args, err := buildSearch(1, &store.ScorecardSearch{TeamUserID: 42}, nil, util.NewPaging(5, 1))
	require.NoError(t, err)

	assert.Contains(t, sql, "(NOT EXISTS (SELECT 1 FROM audit.scorecard_team st")
	assert.Contains(t, sql, "JOIN call_center.cc_team ct", "only teams that still exist count")
	assert.Contains(t, sql, "ORDER BY CASE WHEN EXISTS")
	assert.Contains(t, sql, "THEN 0 ELSE 1 END, s.name, s.id")
	assert.Equal(t, []any{int64(1), int64(42), int64(42)}, args)

	sql, _, err = buildSearch(1, &store.ScorecardSearch{TeamUserID: 42, Sort: "-id"}, nil, util.NewPaging(5, 1))
	require.NoError(t, err)
	assert.Contains(t, sql, "ORDER BY s.id DESC, s.id")
	assert.Contains(t, sql, "NOT EXISTS")
}

func TestBuildSearchUnknownSort(t *testing.T) {
	_, _, err := buildSearch(1, &store.ScorecardSearch{Sort: "questions"}, nil, util.NewPaging(5, 1))
	assert.Equal(t, codes.InvalidArgument, errors.Code(err))
}
