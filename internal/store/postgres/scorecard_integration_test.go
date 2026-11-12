//go:build integration

package postgres

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"google.golang.org/grpc/codes"

	"github.com/webitel/webitel-go-kit/infra/pgw"
	"github.com/webitel/webitel-go-kit/infra/webitel/auth"
	"github.com/webitel/webitel-go-kit/pkg/errors"

	"github.com/webitel/webitel-audit/internal/model"
	"github.com/webitel/webitel-audit/internal/service"
	"github.com/webitel/webitel-audit/internal/store"
	"github.com/webitel/webitel-audit/migrations"
)

// ownerAccess is every access bit: the owner of a new form gets it.
const ownerAccess = 255

// Stubs of the shared Webitel tables the audit migrations and queries read; only the columns used.
const bootstrapSQL = `
CREATE SCHEMA IF NOT EXISTS directory;
CREATE SCHEMA IF NOT EXISTS call_center;
CREATE TABLE IF NOT EXISTS directory.wbt_domain (dc bigint PRIMARY KEY, name text);
CREATE TABLE IF NOT EXISTS directory.wbt_auth (id bigint PRIMARY KEY, dc bigint NOT NULL, name text, UNIQUE (id, dc));
CREATE TABLE IF NOT EXISTS directory.wbt_user (id bigint PRIMARY KEY, dc bigint NOT NULL, name text, username text);
CREATE TABLE IF NOT EXISTS directory.wbt_class (id bigint PRIMARY KEY, dc bigint NOT NULL, name text NOT NULL);
CREATE TABLE IF NOT EXISTS directory.wbt_default_acl (object bigint, grantor bigint, subject bigint, access smallint);
CREATE TABLE IF NOT EXISTS directory.wbt_auth_member (member_id bigint, role_id bigint);
CREATE TABLE IF NOT EXISTS call_center.cc_team (id bigint PRIMARY KEY, domain_id bigint NOT NULL, name varchar NOT NULL);
CREATE TABLE IF NOT EXISTS call_center.cc_agent (user_id bigint, team_id bigint, domain_id bigint);
`

var testPM *pgw.PoolManager

// TestMain uses POSTGRES_DSN (a disposable database: the tests create schemas and rows) or starts
// a testcontainers postgres.
func TestMain(m *testing.M) {
	os.Exit(runIntegration(m))
}

func runIntegration(m *testing.M) int {
	ctx := context.Background()

	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		container, err := tcpostgres.Run(ctx, "postgres:16-alpine", tcpostgres.BasicWaitStrategies())
		if err != nil {
			fmt.Fprintln(os.Stderr, "start postgres container:", err)

			return 1
		}

		defer func() { _ = testcontainers.TerminateContainer(container) }()

		dsn = container.MustConnectionString(ctx, "sslmode=disable")
	}

	if err := migrate(ctx, dsn); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)

		return 1
	}

	// The concurrency tests hold up to 6 connections at once; the pgx default is max(4, NumCPU).
	pm, err := pgw.NewPoolManager(ctx, pgw.WithPrimaryConfig(pgw.PrimaryConfig{DSN: dsn, MaxConns: 10}))
	if err != nil {
		fmt.Fprintln(os.Stderr, "pool:", err)

		return 1
	}
	defer pm.Close()

	testPM = pm

	return m.Run()
}

func migrate(ctx context.Context, dsn string) error {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return err
	}

	db := stdlib.OpenDB(*cfg.ConnConfig)
	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(ctx, bootstrapSQL); err != nil {
		return err
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.EmbedMigrations,
		goose.WithTableName(migrations.VersionTable))
	if err != nil {
		return err
	}

	_, err = provider.Up(ctx)

	return err
}

// The tests share one database and isolate themselves by domain, and waitForLockWaiters counts lock waits of the
// whole database, so they must not call t.Parallel.
//
// fixture is a fresh domain: an owner user, another user (an agent of teamB), a role with the
// default read access to forms created by the owner, and two teams.
type fixture struct {
	dc, owner, other, role int64
	teamA, teamB           int64
}

func newFixture(t *testing.T) fixture {
	t.Helper()

	ctx := context.Background()
	db, err := testPM.Primary()
	require.NoError(t, err)

	var dc int64
	require.NoError(t, db.QueryRow(ctx, `SELECT coalesce(max(dc), 0) + 1 FROM directory.wbt_domain`).Scan(&dc))

	base := dc * 1000
	f := fixture{dc: dc, owner: base + 1, other: base + 2, role: base + 3, teamA: base + 1, teamB: base + 2}

	// A multi-statement text takes no parameters, so the generated ids are inlined.
	_, err = db.Exec(ctx, fmt.Sprintf(`
		INSERT INTO directory.wbt_domain (dc, name) VALUES (%[1]d, 'd%[1]d');
		INSERT INTO directory.wbt_auth (id, dc, name) VALUES (%[2]d, %[1]d, 'owner'), (%[3]d, %[1]d, 'other'), (%[4]d, %[1]d, 'role');
		INSERT INTO directory.wbt_user (id, dc, name, username) VALUES (%[2]d, %[1]d, 'Owner', 'owner'), (%[3]d, %[1]d, NULL, 'other');
		INSERT INTO directory.wbt_class (id, dc, name) VALUES (%[2]d, %[1]d, 'cc_audit_form');
		INSERT INTO directory.wbt_default_acl (object, grantor, subject, access) VALUES (%[2]d, %[2]d, %[4]d, %[7]d);
		INSERT INTO call_center.cc_team (id, domain_id, name) VALUES (%[5]d, %[1]d, 'Alpha'), (%[6]d, %[1]d, 'Beta');
		INSERT INTO call_center.cc_agent (user_id, team_id, domain_id) VALUES (%[3]d, %[6]d, %[1]d);`,
		f.dc, f.owner, f.other, f.role, f.teamA, f.teamB, store.AccessRead))

	require.NoError(t, err)

	return f
}

func newStore() *ScorecardStore { return NewScorecardStore(testPM) }

// sample is a valid form: one group with a Score criterion, an ungrouped Option criterion.
func sample(f fixture, name string) *model.Scorecard {
	threshold := int16(2)
	s := &model.Scorecard{
		Name: name, Description: "desc", Enabled: true,
		InteractionTypes: []model.InteractionType{model.InteractionTypeCall},
		TeamIDs:          []int64{f.teamA},
		CreatedBy:        f.owner, UpdatedBy: f.owner,
		Groups: []model.CriteriaGroup{{Name: "Greeting"}},
		Criteria: []model.Criterion{
			{
				Type: model.CriterionTypeScore, Question: "Said hello", Required: true, Min: 0, Max: 5, Weight: 1.5,
				RequireComment: true, CommentThreshold: &threshold, Description: "how",
			},
			{Type: model.CriterionTypeOption, Question: "Tone", Options: []model.Option{
				{Name: "Good", Score: 2}, {Name: "Bad", Score: -1, RequireComment: true},
			}},
		},
	}
	s.Normalize()
	s.Criteria[0].GroupID = s.Groups[0].ID

	return s
}

func create(t *testing.T, f fixture, name string) *store.ScorecardView {
	t.Helper()

	in := sample(f, name)
	require.NoError(t, in.Validate())

	out, err := newStore().Create(context.Background(), f.dc, in)
	require.NoError(t, err)

	return out
}

// update replaces the whole form as UpdateScorecard does: a Patch whose mutate swaps in the payload.
func update(ctx context.Context, s *ScorecardStore, dc int64, in *model.Scorecard, rbac *store.RBAC) (*store.ScorecardView, error) {
	return s.Patch(ctx, dc, in.ID, rbac, func(sc *model.Scorecard, _ bool) error {
		*sc = *in

		return nil
	})
}

// asOwner is the context of a call by the form owner of f, as the auth interceptor leaves it.
func asOwner(f fixture) context.Context {
	return auth.ContextWithSession(context.Background(), &auth.Session{DomainID: f.dc, UserID: f.owner})
}

// searchNames returns the names Search finds, in its order.
func searchNames(t *testing.T, s *ScorecardStore, dc int64, p store.ScorecardSearch) []string {
	t.Helper()

	items, _, err := s.Search(context.Background(), dc, &p, nil)
	require.NoError(t, err)

	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Name)
	}

	return out
}

func TestScorecardCreateReadRoundTrip(t *testing.T) {
	f := newFixture(t)
	in := sample(f, "Round trip")

	out, err := newStore().Create(context.Background(), f.dc, in)
	require.NoError(t, err)

	got, err := newStore().Read(context.Background(), f.dc, out.ID, nil)
	require.NoError(t, err)

	assert.Equal(t, "Round trip", got.Name)
	assert.True(t, got.Editable)
	assert.Equal(t, &store.Lookup{ID: f.owner, Name: "Owner"}, got.Creator)
	assert.Equal(t, []store.Lookup{{ID: f.teamA, Name: "Alpha"}}, got.Teams)
	assert.Equal(t, []int64{f.teamA}, got.TeamIDs)
	assert.Equal(t, in.Groups, got.Groups)
	require.Len(t, got.Criteria, 2)
	assert.Equal(t, in.Criteria, got.Criteria)
	assert.True(t, in.StructureEqual(&got.Scorecard))
	assert.Equal(t, in.ContentHash(), got.ContentHash())
}

func TestScorecardCreateACL(t *testing.T) {
	f := newFixture(t)
	out := create(t, f, "ACL")

	db, err := testPM.Primary()
	require.NoError(t, err)

	rows, err := db.Query(context.Background(),
		`SELECT subject, access FROM audit.scorecard_acl WHERE object = $1 ORDER BY subject`, out.ID)
	require.NoError(t, err)

	got := map[int64]int16{}

	for rows.Next() {
		var (
			subject int64
			access  int16
		)

		require.NoError(t, rows.Scan(&subject, &access))
		got[subject] = access
	}

	require.NoError(t, rows.Err())
	// Owner gets full access; the role gets its default read access of the cc_audit_form class.
	assert.Equal(t, map[int64]int16{f.owner: ownerAccess, f.role: store.AccessRead}, got)
}

func TestScorecardCreateUnknownTeam(t *testing.T) {
	f := newFixture(t)
	g := newFixture(t)
	in := sample(f, "Foreign team")
	in.TeamIDs = []int64{g.teamA}

	_, err := newStore().Create(context.Background(), f.dc, in)
	assert.Equal(t, codes.InvalidArgument, errors.Code(err))
	assert.Equal(t, "audit.scorecard.team_unknown", errors.ID(err))
}

func TestScorecardDuplicateName(t *testing.T) {
	f := newFixture(t)
	create(t, f, "Twice")

	_, err := newStore().Create(context.Background(), f.dc, sample(f, "Twice"))
	assert.Equal(t, codes.AlreadyExists, errors.Code(err))
	assert.Equal(t, "audit.scorecard.name_exists", errors.ID(err))
}

func TestScorecardDomainIsolation(t *testing.T) {
	f := newFixture(t)
	g := newFixture(t)
	out := create(t, f, "Isolated")
	s := newStore()
	ctx := context.Background()

	_, err := s.Read(ctx, g.dc, out.ID, nil)
	assert.Equal(t, codes.NotFound, errors.Code(err))

	upd := sample(g, "Hijack")
	upd.ID = out.ID
	upd.TeamIDs = nil
	_, err = update(ctx, s, g.dc, upd, nil)
	assert.Equal(t, codes.NotFound, errors.Code(err))

	_, err = s.Delete(ctx, g.dc, out.ID, nil)
	assert.Equal(t, codes.NotFound, errors.Code(err))

	items, _, err := s.Search(ctx, g.dc, &store.ScorecardSearch{}, nil)
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestScorecardRBAC(t *testing.T) {
	f := newFixture(t)
	out := create(t, f, "RBAC")
	s := newStore()
	ctx := context.Background()

	_, err := s.Read(ctx, f.dc, out.ID, &store.RBAC{Roles: []int64{f.role}})
	require.NoError(t, err)

	_, err = s.Read(ctx, f.dc, out.ID, &store.RBAC{Roles: []int64{f.other}})
	assert.Equal(t, codes.NotFound, errors.Code(err))

	// The role has read only: update and delete are hidden.
	upd := sample(f, "RBAC")
	upd.ID = out.ID
	_, err = update(ctx, s, f.dc, upd, &store.RBAC{Roles: []int64{f.role}})
	assert.Equal(t, codes.NotFound, errors.Code(err))

	_, err = s.Delete(ctx, f.dc, out.ID, &store.RBAC{Roles: []int64{f.role}})
	assert.Equal(t, codes.NotFound, errors.Code(err))

	items, _, err := s.Search(ctx, f.dc, &store.ScorecardSearch{}, &store.RBAC{Roles: []int64{f.other}})
	require.NoError(t, err)
	assert.Empty(t, items)

	items, _, err = s.Search(ctx, f.dc, &store.ScorecardSearch{}, &store.RBAC{Roles: []int64{f.owner}})
	require.NoError(t, err)
	assert.Len(t, items, 1)
}

func TestScorecardSearch(t *testing.T) {
	f := newFixture(t)
	s := newStore()
	ctx := context.Background()

	a := sample(f, "Alpha form")
	a.TeamIDs = []int64{f.teamA}
	a.InteractionTypes = []model.InteractionType{model.InteractionTypeCall}
	b := sample(f, "Beta form")
	b.Description = "100% off"
	b.TeamIDs = []int64{f.teamB}
	b.InteractionTypes = []model.InteractionType{model.InteractionTypeChat, model.InteractionTypeEmail}
	b.Enabled = false
	b.Criteria[1].Question = "Farewell"
	c := sample(f, "Common form")
	c.TeamIDs = nil
	c.Archive = true

	ids := map[string]int32{}

	for _, in := range []*model.Scorecard{a, b, c} {
		out, err := s.Create(ctx, f.dc, in)
		require.NoError(t, err)

		ids[in.Name] = out.ID
	}

	db, err := testPM.Primary()
	require.NoError(t, err)

	_, err = db.Exec(ctx, `UPDATE audit.scorecard SET editable = false WHERE id = $1`, ids["Common form"])
	require.NoError(t, err)

	yes, no := true, false

	// Default order: newest first.
	filters := []struct {
		name   string
		search store.ScorecardSearch
		want   []string
	}{
		{"default order", store.ScorecardSearch{}, []string{"Common form", "Beta form", "Alpha form"}},
		{"q with literal percent", store.ScorecardSearch{Q: "100%*"}, []string{"Beta form"}},
		{"ids", store.ScorecardSearch{IDs: []int32{ids["Common form"]}}, []string{"Common form"}},
		{"team", store.ScorecardSearch{TeamIDs: []int64{f.teamB}}, []string{"Beta form"}},
		{"disabled", store.ScorecardSearch{Enabled: &no}, []string{"Beta form"}},
		{"not editable", store.ScorecardSearch{Editable: &no}, []string{"Common form"}},
		{"archived", store.ScorecardSearch{Archive: &yes}, []string{"Common form"}},
		{"criterion text", store.ScorecardSearch{Question: "farew*"}, []string{"Beta form"}},
		{"sort by name", store.ScorecardSearch{Sort: "+name"}, []string{"Alpha form", "Beta form", "Common form"}},
		// Confluence "Е2. Scorecards", AC_02.01.04: interaction type filter matches any of the chosen types.
		{"any interaction type", store.ScorecardSearch{
			InteractionTypes: []model.InteractionType{model.InteractionTypeCall, model.InteractionTypeEmail}, Archive: &no,
		}, []string{"Beta form", "Alpha form"}},
		// Engine team_filter: forms of the user's teams first, then forms without teams; other teams' forms hidden.
		{"team filter", store.ScorecardSearch{TeamUserID: f.other}, []string{"Beta form", "Common form"}},
	}

	for _, tc := range filters {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, searchNames(t, s, f.dc, tc.search))
		})
	}

	t.Run("pagination", func(t *testing.T) {
		page, next, err := s.Search(ctx, f.dc, &store.ScorecardSearch{Size: 2, Page: 1}, nil)
		require.NoError(t, err)
		assert.Len(t, page, 2)
		assert.True(t, next)

		page, next, err = s.Search(ctx, f.dc, &store.ScorecardSearch{Size: 2, Page: 2}, nil)
		require.NoError(t, err)
		assert.Len(t, page, 1)
		assert.False(t, next)
	})

	t.Run("fields", func(t *testing.T) {
		// Default fields leave the children out; asking for them loads them.
		page, _, err := s.Search(ctx, f.dc, &store.ScorecardSearch{IDs: []int32{ids["Alpha form"]}}, nil)
		require.NoError(t, err)
		require.Len(t, page, 1)
		assert.Empty(t, page[0].Criteria)
		assert.Len(t, page[0].Teams, 1)

		page, _, err = s.Search(ctx, f.dc, &store.ScorecardSearch{IDs: []int32{ids["Alpha form"]}, Fields: []string{"criteria,groups"}}, nil)
		require.NoError(t, err)
		require.Len(t, page, 1)
		assert.Len(t, page[0].Criteria, 2)
		assert.Len(t, page[0].Criteria[1].Options, 2)
		assert.Len(t, page[0].Groups, 1)
		assert.Empty(t, page[0].Teams)

		_, _, err = s.Search(ctx, f.dc, &store.ScorecardSearch{Fields: []string{"bogus"}}, nil)
		assert.Equal(t, codes.InvalidArgument, errors.Code(err))
	})
}

// Engine cc_audit_form GetAllPage with ReplaceWebSearch: `*` is the only wildcard, ILIKE, no implicit %…%.
func TestScorecardSearchWildcard(t *testing.T) {
	f := newFixture(t)
	s := newStore()
	ctx := context.Background()

	for _, name := range []string{"test", "Testing form", "Latest", "Other", "te%st", "te_st"} {
		in := sample(f, name)
		in.Description = ""
		in.Criteria[0].Question = "Q " + name
		_, err := s.Create(ctx, f.dc, in)
		require.NoError(t, err)
	}

	// Sorted here, not by the database collation.
	names := func(p store.ScorecardSearch) []string {
		return slices.Sorted(slices.Values(searchNames(t, s, f.dc, p)))
	}

	assert.Equal(t, []string{"Testing form", "te%st", "te_st", "test"}, names(store.ScorecardSearch{Q: "te*"}),
		"prefix, case-insensitive")
	assert.Equal(t, []string{"Latest", "Testing form", "te%st", "te_st", "test"}, names(store.ScorecardSearch{Q: "*st*"}),
		"contains")
	assert.Equal(t, []string{"test"}, names(store.ScorecardSearch{Q: "TEST"}), "no wildcard: the whole name")
	assert.Equal(t, []string{"te%st"}, names(store.ScorecardSearch{Q: "te%st"}), "a typed % is literal")
	assert.Equal(t, []string{"te_st"}, names(store.ScorecardSearch{Q: "te_*"}), "a typed _ is literal")
	assert.Len(t, names(store.ScorecardSearch{}), 6, "empty q does not filter")
	assert.Equal(t, []string{"Latest"}, names(store.ScorecardSearch{Question: "*latest"}), "question takes the same pattern")
	assert.Empty(t, names(store.ScorecardSearch{Question: "latest"}))
}

// The frontend list call: duplicated id, no teams or criteria, an empty sort.
func TestScorecardSearchFrontendFields(t *testing.T) {
	f := newFixture(t)
	s := newStore()
	ctx := context.Background()

	older := create(t, f, "Older")
	newer := create(t, f, "Newer")

	items, next, err := s.Search(ctx, f.dc, &store.ScorecardSearch{
		Page: 1, Size: 10, Sort: "",
		Fields: []string{
			"id", "editable", "id", "name", "description", "created_at", "created_by", "updated_at", "updated_by", "enabled",
		},
	}, nil)
	require.NoError(t, err)
	assert.False(t, next)
	require.Len(t, items, 2)
	assert.Equal(t, []int32{newer.ID, older.ID}, []int32{items[0].ID, items[1].ID}, "default order: newest first")

	for _, it := range items {
		assert.NotEmpty(t, it.Name)
		assert.True(t, it.Editable)
		assert.Equal(t, f.owner, it.CreatedBy)
		assert.Empty(t, it.Teams, "teams not asked")
		assert.Empty(t, it.TeamIDs)
		assert.Empty(t, it.Groups)
		assert.Empty(t, it.Criteria)
	}
}

func TestScorecardUpdateReplacesChildren(t *testing.T) {
	f := newFixture(t)
	out := create(t, f, "Update")
	s := newStore()
	ctx := context.Background()

	upd := out.Scorecard
	upd.Name = "Updated"
	upd.UpdatedBy = f.other
	upd.TeamIDs = []int64{f.teamB}
	// Confluence "Е2. Scorecards", US_2.9: deleting a group keeps its criteria in the form without a group.
	upd.Groups = nil
	upd.Criteria = []model.Criterion{out.Criteria[0], {Type: model.CriterionTypeYes, Question: "New"}}
	upd.Criteria[0].GroupID = uuid.Nil
	upd.Normalize()
	require.NoError(t, upd.Validate())

	got, err := update(ctx, s, f.dc, &upd, nil)
	require.NoError(t, err)

	assert.Equal(t, "Updated", got.Name)
	assert.Equal(t, &store.Lookup{ID: f.other, Name: "other"}, got.Updater)
	assert.Equal(t, []int64{f.teamB}, got.TeamIDs)
	assert.Empty(t, got.Groups)
	require.Len(t, got.Criteria, 2)
	assert.Equal(t, out.Criteria[0].ID, got.Criteria[0].ID)
	assert.Equal(t, uuid.Nil, got.Criteria[0].GroupID)
	assert.Equal(t, "New", got.Criteria[1].Question)
}

func TestScorecardUpdateRejectsForeignIDs(t *testing.T) {
	f := newFixture(t)
	victim := create(t, f, "Victim")
	out := create(t, f, "Attacker")
	s := newStore()
	ctx := context.Background()

	for name, mutate := range map[string]func(*model.Scorecard){
		"criterion": func(u *model.Scorecard) { u.Criteria[0].ID = victim.Criteria[0].ID },
		"group": func(u *model.Scorecard) {
			u.Groups[0].ID = victim.Groups[0].ID
			u.Criteria[0].GroupID = victim.Groups[0].ID
		},
		"option": func(u *model.Scorecard) { u.Criteria[1].Options[0].ID = victim.Criteria[1].Options[0].ID },
	} {
		t.Run(name, func(t *testing.T) {
			upd := out.Clone()
			mutate(&upd)

			_, err := update(ctx, s, f.dc, &upd, nil)
			assert.Equal(t, codes.InvalidArgument, errors.Code(err))
			assert.Equal(t, "audit.scorecard.foreign_id", errors.ID(err))
		})
	}

	got, err := s.Read(ctx, f.dc, victim.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, victim.Criteria, got.Criteria, "the victim form is untouched")
}

// The frozen-structure rule lives in the service: these go through it, on the real store.
// The steps run in order and share the form.
func TestScorecardNotEditable(t *testing.T) {
	f := newFixture(t)
	out := create(t, f, "Frozen")
	s := newStore()
	svc := service.NewScorecardService(s)
	ctx := asOwner(f)

	db, err := testPM.Primary()
	require.NoError(t, err)
	_, err = db.Exec(ctx, `UPDATE audit.scorecard SET editable = false WHERE id = $1`, out.ID)
	require.NoError(t, err)

	// Confluence "Е2. Scorecards", AC_02.12.02: a form with evaluations keeps its structure...
	t.Run("structural update is refused", func(t *testing.T) {
		structural := out.Clone()
		structural.Criteria[0].Weight = 2

		_, err := svc.Update(ctx, &structural)
		assert.Equal(t, codes.PermissionDenied, errors.Code(err))
		assert.Equal(t, "audit.scorecard.not_editable", errors.ID(err))
	})

	// Confluence "Е2. Scorecards", AC_02.12.02: Interaction type is a structural field too.
	t.Run("interaction type is structural", func(t *testing.T) {
		otherType := out.Clone()
		otherType.InteractionTypes = []model.InteractionType{model.InteractionTypeChat}

		_, err := svc.Update(ctx, &otherType)
		assert.Equal(t, "audit.scorecard.not_editable", errors.ID(err))
	})

	// Confluence "Е2. Scorecards", AC_02.12.01: name, description, teams, criterion descriptions and group
	// names stay editable (state and archive are not content).
	var got *store.ScorecardView

	t.Run("descriptive update is allowed", func(t *testing.T) {
		descriptive := out.Clone()
		descriptive.Name = "Frozen renamed"
		descriptive.Archive = true
		descriptive.TeamIDs = []int64{f.teamB}
		descriptive.Groups[0].Name = "Opening"
		descriptive.Criteria[0].Description = "new hint"

		var err error

		got, err = svc.Update(ctx, &descriptive)
		require.NoError(t, err)
		assert.Equal(t, "Frozen renamed", got.Name)
		assert.Equal(t, "Opening", got.Groups[0].Name)
		assert.Equal(t, "new hint", got.Criteria[0].Description)
		assert.False(t, got.Editable)
	})

	t.Run("structural patch is refused", func(t *testing.T) {
		require.NotNil(t, got)

		heavier := got.Clone()
		heavier.Criteria[0].Weight = 3

		_, err := svc.Patch(ctx, &heavier, []string{service.MaskCriteria})
		assert.Equal(t, "audit.scorecard.not_editable", errors.ID(err))
	})

	t.Run("descriptive patch is allowed", func(t *testing.T) {
		require.NotNil(t, got)

		renamed := got.Clone()
		renamed.Description = "patched"
		renamed.Groups[0].Name = "Start"

		patched, err := svc.Patch(ctx, &renamed, []string{service.MaskDescription, service.MaskGroups})
		require.NoError(t, err)
		assert.Equal(t, "patched", patched.Description)
		assert.Equal(t, "Start", patched.Groups[0].Name)
	})

	// Confluence "Е2. Scorecards", AC_02.02.03 / AC_02.14.01: a scorecard with evaluations cannot be deleted.
	t.Run("delete is refused", func(t *testing.T) {
		_, err := s.Delete(ctx, f.dc, out.ID, nil)
		assert.Equal(t, codes.PermissionDenied, errors.Code(err))
		assert.Equal(t, "audit.scorecard.not_editable", errors.ID(err))
	})
}

// A form copied from engine as is: Say "Yes" required and critical without require_comment, critical_violation
// on Score and Option. A descriptive PATCH/PUT of it, used, is not structural: the stored form is compared
// normalized (Confluence "Е2. Scorecards", AC_02.15.02: Required is cleared for Say "Yes").
func TestScorecardLegacyNotEditableDescriptiveChange(t *testing.T) {
	f := newFixture(t)
	s := newStore()
	svc := service.NewScorecardService(s)
	ctx := asOwner(f)

	db, err := testPM.Primary()
	require.NoError(t, err)

	var id int32
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO audit.scorecard (domain_id, name, enabled, editable, interaction_type, created_by)
		VALUES ($1, 'Legacy', true, false, ARRAY[$2::smallint], $3) RETURNING id`,
		f.dc, model.InteractionTypeCall, f.owner).Scan(&id))

	_, err = db.Exec(ctx, fmt.Sprintf(`WITH c AS (
			INSERT INTO audit.scorecard_criterion (scorecard_id, position, type, question, required, critical_violation,
				require_comment, min_score, max_score)
			VALUES ($1, 0, %[1]d, 'Score', true, true, false, 0, 5),
				($1, 1, %[2]d, 'Option', false, true, false, NULL, NULL),
				($1, 2, %[3]d, 'Yes', true, true, false, NULL, NULL)
			RETURNING id, type)
		INSERT INTO audit.scorecard_criterion_option (criterion_id, position, name, score)
		SELECT id, 0, 'Good', 1 FROM c WHERE type = %[2]d`,
		model.CriterionTypeScore, model.CriterionTypeOption, model.CriterionTypeYes), id)
	require.NoError(t, err)

	got, err := svc.Patch(ctx, &model.Scorecard{ID: id, Name: "Legacy renamed"}, []string{service.MaskName})
	require.NoError(t, err)
	assert.Equal(t, "Legacy renamed", got.Name)
	assert.False(t, got.Criteria[2].Required, "stored normalized")
	assert.True(t, got.Criteria[2].RequireComment)
	assert.False(t, got.Criteria[0].CriticalViolation)

	// A structural change is still refused.
	changed := got.Clone()
	changed.Criteria[0].Max = 4

	_, err = svc.Patch(ctx, &changed, []string{service.MaskCriteria})
	assert.Equal(t, "audit.scorecard.not_editable", errors.ID(err))
}

func TestScorecardDelete(t *testing.T) {
	f := newFixture(t)
	out := create(t, f, "Delete me")
	s := newStore()
	ctx := context.Background()

	got, err := s.Delete(ctx, f.dc, out.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, out.Criteria, got.Criteria)

	_, err = s.Read(ctx, f.dc, out.ID, nil)
	assert.Equal(t, codes.NotFound, errors.Code(err))

	_, err = s.Delete(ctx, f.dc, out.ID, nil)
	assert.Equal(t, codes.NotFound, errors.Code(err))
	assert.Equal(t, "audit.scorecard.not_found", errors.ID(err))
}

func TestScorecardUpsertSnapshot(t *testing.T) {
	f := newFixture(t)
	out := create(t, f, "Snapshot")
	s := newStore()
	ctx := context.Background()

	first, err := s.UpsertSnapshot(ctx, f.dc, out.ID)
	require.NoError(t, err)

	same, err := s.UpsertSnapshot(ctx, f.dc, out.ID)
	require.NoError(t, err)
	assert.Equal(t, first, same)

	upd := out.Clone()
	upd.Criteria[0].Weight = 3
	_, err = update(ctx, s, f.dc, &upd, nil)
	require.NoError(t, err)

	changed, err := s.UpsertSnapshot(ctx, f.dc, out.ID)
	require.NoError(t, err)
	assert.NotEqual(t, first, changed)

	_, err = s.UpsertSnapshot(ctx, newFixture(t).dc, out.ID)
	assert.Equal(t, codes.NotFound, errors.Code(err))
}

// waitForLockWaiters reports whether n other backends wait on a lock while running a query that
// contains like; it gives up after 10s.
func waitForLockWaiters(t *testing.T, n int, like string) bool {
	t.Helper()

	db, err := testPM.Primary()
	require.NoError(t, err)

	return assert.Eventually(t, func() bool {
		var waiting int

		err := db.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND pid <> pg_backend_pid()
				AND wait_event_type = 'Lock' AND query LIKE '%' || $1 || '%'`, like).Scan(&waiting)

		return err == nil && waiting >= n
	}, 10*time.Second, 20*time.Millisecond, "%d backend(s) waiting on a lock in %q", n, like)
}

func TestScorecardConcurrentPatchesBothSurvive(t *testing.T) {
	f := newFixture(t)
	out := create(t, f, "Concurrent")
	s := newStore()
	ctx := context.Background()

	addCriterion := func(sc *model.Scorecard, _ bool) error {
		sc.Criteria = append(sc.Criteria, model.Criterion{Type: model.CriterionTypeYes, Question: "Added"})
		sc.Normalize()

		return sc.Validate()
	}

	var (
		wg        sync.WaitGroup
		secondErr error
	)

	// The first patch holds the row lock until the second is seen waiting on it, so the second always
	// reads the form after the first commits. Without FOR UPDATE nothing waits and the rename is lost.
	rename := func(sc *model.Scorecard, _ bool) error {
		wg.Go(func() { _, secondErr = s.Patch(ctx, f.dc, out.ID, nil, addCriterion) })
		require.True(t, waitForLockWaiters(t, 1, "FOR UPDATE OF s"))

		sc.Name = "Concurrent renamed"

		return nil
	}

	_, err := s.Patch(ctx, f.dc, out.ID, nil, rename)
	require.NoError(t, err)
	wg.Wait()
	require.NoError(t, secondErr)

	got, err := s.Read(ctx, f.dc, out.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, "Concurrent renamed", got.Name)
	require.Len(t, got.Criteria, 3)
	assert.Equal(t, "Added", got.Criteria[2].Question)
}

// Patch needs only the update bit, not read.
func TestScorecardPatchRBAC(t *testing.T) {
	f := newFixture(t)
	out := create(t, f, "Patch RBAC")
	s := newStore()
	ctx := context.Background()

	db, err := testPM.Primary()
	require.NoError(t, err)
	_, err = db.Exec(ctx, `INSERT INTO audit.scorecard_acl (dc, object, subject, access) VALUES ($1, $2, $3, $4)`,
		f.dc, out.ID, f.other, store.AccessUpdate)
	require.NoError(t, err)

	rename := func(sc *model.Scorecard, _ bool) error {
		sc.Name = "Patched by updater"

		return nil
	}

	_, err = s.Patch(ctx, f.dc, out.ID, &store.RBAC{Roles: []int64{f.other}}, rename)
	require.NoError(t, err)

	_, err = s.Patch(ctx, f.dc, out.ID, &store.RBAC{Roles: []int64{f.role}}, rename)
	assert.Equal(t, codes.NotFound, errors.Code(err), "read-only role cannot patch")
}

func TestScorecardGroupNamesCaseSensitive(t *testing.T) {
	f := newFixture(t)
	in := sample(f, "Case")
	in.Groups = append(in.Groups, model.CriteriaGroup{Name: "GREETING"})
	in.Normalize()
	require.NoError(t, in.Validate())

	out, err := newStore().Create(context.Background(), f.dc, in)
	require.NoError(t, err)
	assert.Len(t, out.Groups, 2)
}

func TestScorecardCreatedByRequired(t *testing.T) {
	f := newFixture(t)
	in := sample(f, "No creator")
	in.CreatedBy = 0

	// The store writes NULL for a zero creator, so the NOT NULL constraint is what rejects it.
	_, err := newStore().Create(context.Background(), f.dc, in)
	assert.Equal(t, codes.InvalidArgument, errors.Code(err))
	assert.Equal(t, "store.pg.not_null", errors.ID(err))
	assert.Equal(t, "created_by", errors.Value(err, "column"))
}

// TestScorecardUpsertSnapshotConcurrent: callers racing an uncommitted insert of the same snapshot all get
// the holder's row. The old DO NOTHING + SELECT passes it too (checked), so it guards the result, not the query.
func TestScorecardUpsertSnapshotConcurrent(t *testing.T) {
	f := newFixture(t)
	out := create(t, f, "Snapshot race")
	s := newStore()
	ctx := context.Background()

	db, err := testPM.Primary()
	require.NoError(t, err)

	current, err := readOne(ctx, db, f.dc, out.ID, nil)
	require.NoError(t, err)

	holder, err := db.Begin(ctx)
	require.NoError(t, err)

	defer func() { _ = holder.Rollback(ctx) }()

	var held int64
	require.NoError(t, holder.QueryRow(ctx, `INSERT INTO audit.scorecard_snapshot (scorecard_id, domain_id, hash, content)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		out.ID, f.dc, current.ContentHash(), string(current.SnapshotContent())).Scan(&held))

	const n = 4

	var wg sync.WaitGroup

	ids := make([]int64, n)
	errs := make([]error, n)

	for i := range n {
		wg.Go(func() { ids[i], errs[i] = s.UpsertSnapshot(ctx, f.dc, out.ID) })
	}

	require.True(t, waitForLockWaiters(t, n, "scorecard_snapshot"))
	require.NoError(t, holder.Commit(ctx))
	wg.Wait()

	for i := range n {
		require.NoError(t, errs[i])
		assert.Equal(t, held, ids[i], "same content, the held snapshot")
	}
}

// A team deleted from call_center must not block saving the form: it is dropped from the form on read.
func TestScorecardDeletedTeamDoesNotBlockSave(t *testing.T) {
	f := newFixture(t)
	out := create(t, f, "Deleted team")
	s := newStore()
	ctx := context.Background()

	db, err := testPM.Primary()
	require.NoError(t, err)
	_, err = db.Exec(ctx, `DELETE FROM call_center.cc_team WHERE id = $1`, f.teamA)
	require.NoError(t, err)

	got, err := s.Read(ctx, f.dc, out.ID, nil)
	require.NoError(t, err)
	assert.Empty(t, got.TeamIDs)

	renamed, err := s.Patch(ctx, f.dc, out.ID, nil, func(sc *model.Scorecard, _ bool) error {
		sc.Name = "Renamed"

		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, "Renamed", renamed.Name)
	assert.Empty(t, renamed.TeamIDs)
}

// A team deleted from call_center is gone for the filters too, as it is on read: the form counts as
// a form without teams, not as a form of a team nobody can be in.
func TestScorecardSearchIgnoresDeletedTeams(t *testing.T) {
	f := newFixture(t)
	s := newStore()
	ctx := context.Background()

	onlyA := sample(f, "Only A")
	onlyA.TeamIDs = []int64{f.teamA}
	_, err := s.Create(ctx, f.dc, onlyA)
	require.NoError(t, err)

	db, err := testPM.Primary()
	require.NoError(t, err)
	_, err = db.Exec(ctx, `DELETE FROM call_center.cc_team WHERE id = $1`, f.teamA)
	require.NoError(t, err)

	assert.Empty(t, searchNames(t, s, f.dc, store.ScorecardSearch{TeamIDs: []int64{f.teamA}}), "by a deleted team")
	// f.other is an agent of teamB only: the form has no live team, so it is visible like a form without teams.
	assert.Equal(t, []string{"Only A"}, searchNames(t, s, f.dc, store.ScorecardSearch{TeamUserID: f.other}))
}

// The group name is unique per form, checked at the commit: names may move between the groups of one save.
func TestScorecardGroupNamesMoveWithinOneSave(t *testing.T) {
	f := newFixture(t)
	s := newStore()
	ctx := context.Background()

	in := sample(f, "Names")
	in.Groups = []model.CriteriaGroup{{Name: "A"}, {Name: "B"}}
	in.Criteria[0].GroupID = uuid.Nil
	in.Normalize()
	require.NoError(t, in.Validate())

	out, err := s.Create(ctx, f.dc, in)
	require.NoError(t, err)

	groupNames := func(v *store.ScorecardView) []string {
		names := make([]string, 0, len(v.Groups))
		for _, g := range v.Groups {
			names = append(names, g.Name)
		}

		return names
	}

	t.Run("swap", func(t *testing.T) {
		upd := out.Clone()
		upd.Groups[0].Name, upd.Groups[1].Name = "B", "A"

		got, err := update(ctx, s, f.dc, &upd, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"B", "A"}, groupNames(got))
		out = got
	})

	t.Run("rename and add a group with the old name", func(t *testing.T) {
		upd := out.Clone()
		upd.Groups[0].Name = "C"
		upd.Groups = append(upd.Groups, model.CriteriaGroup{ID: uuid.New(), Name: "B"})
		upd.Normalize()
		require.NoError(t, upd.Validate())

		got, err := update(ctx, s, f.dc, &upd, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"C", "A", "B"}, groupNames(got))
		out = got
	})

	t.Run("a duplicate name is still refused at the commit", func(t *testing.T) {
		upd := out.Clone()
		upd.Groups[0].Name = upd.Groups[1].Name

		_, err := update(ctx, s, f.dc, &upd, nil)
		assert.Equal(t, codes.AlreadyExists, errors.Code(err))
	})
}
