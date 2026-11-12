package postgres

import (
	"context"
	stderrors "errors"
	"slices"
	"strings"

	"github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/webitel/webitel-go-kit/infra/pgw"
	"github.com/webitel/webitel-go-kit/pkg/errors"

	"github.com/webitel/webitel-audit/internal/model"
	"github.com/webitel/webitel-audit/internal/store"
	"github.com/webitel/webitel-audit/internal/store/util"
)

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// txBeginner is what snapshotRead needs from a pool.
type txBeginner interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// snapshotRead runs fn in one read-only REPEATABLE READ transaction: Read, Search and the snapshot load a
// scorecard with several queries, and a concurrent save committing between them would give a torn form.
func snapshotRead[T any](ctx context.Context, db txBeginner, fn func(q querier) (T, error)) (T, error) {
	var out T

	err := pgx.BeginTxFunc(ctx, db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error

		out, err = fn(tx)

		return err
	})

	return out, ParseError(err)
}

// liveTeam opens a subquery over the teams of a form that still exist in call_center (a deleted team's row stays in scorecard_team until the next save); the caller appends `AND ...)` or `)`.
const liveTeam = `SELECT 1 FROM audit.scorecard_team st
	JOIN call_center.cc_team ct ON ct.id = st.team_id AND ct.domain_id = st.domain_id
	WHERE st.scorecard_id = s.id `

var psql = squirrel.StatementBuilder.PlaceholderFormat(squirrel.Dollar)

const (
	scorecardDuplicateNameConstraint = "scorecard_domain_name_key"
	defaultScorecardSort             = "-created_at"
)

var (
	errScorecardNotFound    = errors.NotFound("scorecard not found", errors.WithID("audit.scorecard.not_found"))
	errScorecardTeamUnknown = errors.InvalidArgument("team is unknown in this domain",
		errors.WithID("audit.scorecard.team_unknown"))
	// Ids are global uuid keys: a payload must not take over rows of another scorecard.
	errScorecardForeignID = errors.InvalidArgument("id belongs to another scorecard",
		errors.WithID("audit.scorecard.foreign_id"))
)

var scorecardSortable = map[string]string{
	"id":          "s.id",
	"name":        "s.name",
	"description": "s.description",
	"enabled":     "s.enabled",
	"archive":     "s.archive",
	"editable":    "s.editable",
	"created_at":  "s.created_at",
	"updated_at":  "s.updated_at",
}

// Field names of ScorecardSearch.Fields; only the last three change what is loaded.
const (
	fieldTeams    = "teams"
	fieldGroups   = "groups"
	fieldCriteria = "criteria"
)

var (
	scorecardFields = []string{
		"id", "name", "description", "enabled", "archive", "editable", "interaction_type",
		"created_at", "created_by", "updated_at", "updated_by", fieldTeams, fieldGroups, fieldCriteria,
	}
	scorecardDefaultFields = slices.DeleteFunc(slices.Clone(scorecardFields),
		func(f string) bool { return f == fieldGroups || f == fieldCriteria })
)

// ScorecardStore implements store.ScorecardStore on the audit schema.
type ScorecardStore struct {
	pm *pgw.PoolManager
}

var _ store.ScorecardStore = (*ScorecardStore)(nil)

func NewScorecardStore(pm *pgw.PoolManager) *ScorecardStore {
	return &ScorecardStore{pm: pm}
}

func (s *ScorecardStore) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	db, err := s.pm.Primary()
	if err != nil {
		return ParseError(err)
	}

	return ParseError(pgx.BeginFunc(ctx, db, fn))
}

func (s *ScorecardStore) Create(ctx context.Context, domainID int64, in *model.Scorecard) (*store.ScorecardView, error) {
	var out *store.ScorecardView

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		// editable keeps its default (true); the trigger scorecard_set_rbac_acl writes the owner and role ACL.
		sql, args, err := psql.Insert("audit.scorecard").
			Columns("domain_id", "name", "description", "enabled", "archive", "interaction_type", "created_by", "updated_by").
			Values(domainID, in.Name, nullIfEmpty(in.Description), in.Enabled, in.Archive,
				in.InteractionTypes, nullIfZero(in.CreatedBy), nullIfZero(in.CreatedBy)).
			Suffix("RETURNING id").
			ToSql()
		if err != nil {
			return err
		}

		var id int32
		if err := tx.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			return parseScorecardError(err)
		}

		if err := writeChildren(ctx, tx, domainID, id, in); err != nil {
			return err
		}

		out, err = readOne(ctx, tx, domainID, id, nil)

		return err
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

func (s *ScorecardStore) Read(ctx context.Context, domainID int64, id int32, rbac *store.RBAC) (*store.ScorecardView, error) {
	db, err := s.pm.StandbyPreferred()
	if err != nil {
		return nil, ParseError(err)
	}

	return snapshotRead(ctx, db, func(q querier) (*store.ScorecardView, error) {
		return readOne(ctx, q, domainID, id, rbac)
	})
}

func (s *ScorecardStore) Search(
	ctx context.Context, domainID int64, params *store.ScorecardSearch, rbac *store.RBAC,
) ([]*store.ScorecardView, bool, error) {
	db, err := s.pm.StandbyPreferred()
	if err != nil {
		return nil, false, ParseError(err)
	}

	paging := util.NewPaging(params.Size, params.Page)

	items, err := snapshotRead(ctx, db, func(q querier) ([]*store.ScorecardView, error) {
		return search(ctx, q, domainID, params, rbac, paging)
	})
	if err != nil {
		return nil, false, err
	}

	items, next := util.ResolvePaging(paging, items)

	return items, next, nil
}

func (s *ScorecardStore) Patch(
	ctx context.Context, domainID int64, id int32, rbac *store.RBAC, mutate func(sc *model.Scorecard, editable bool) error,
) (*store.ScorecardView, error) {
	var out *store.ScorecardView

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		editable, err := lockScorecard(ctx, tx, domainID, id, rbac, store.AccessUpdate)
		if err != nil {
			return err
		}

		current, err := readOne(ctx, tx, domainID, id, nil)
		if err != nil {
			return err
		}

		next := &current.Scorecard
		if err := mutate(next, editable); err != nil {
			return err
		}

		next.ID = id
		out, err = save(ctx, tx, domainID, next)

		return err
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// save writes in over the locked scorecard in.ID and reads it back.
func save(ctx context.Context, tx querier, domainID int64, in *model.Scorecard) (*store.ScorecardView, error) {
	sql, args, err := psql.Update("audit.scorecard").
		Set("name", in.Name).
		Set("description", nullIfEmpty(in.Description)).
		Set("enabled", in.Enabled).
		Set("archive", in.Archive).
		Set("interaction_type", in.InteractionTypes).
		Set("updated_at", squirrel.Expr("now()")).
		Set("updated_by", nullIfZero(in.UpdatedBy)).
		Where("id = ? AND domain_id = ?", in.ID, domainID).
		ToSql()
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		return nil, parseScorecardError(err)
	}

	if err := writeChildren(ctx, tx, domainID, in.ID, in); err != nil {
		return nil, err
	}

	return readOne(ctx, tx, domainID, in.ID, nil)
}

func (s *ScorecardStore) Delete(ctx context.Context, domainID int64, id int32, rbac *store.RBAC) (*store.ScorecardView, error) {
	var out *store.ScorecardView

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		editable, err := lockScorecard(ctx, tx, domainID, id, rbac, store.AccessDelete)
		if err != nil {
			return err
		}

		if !editable {
			return model.ErrNotEditable
		}

		if out, err = readOne(ctx, tx, domainID, id, nil); err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `DELETE FROM audit.scorecard WHERE id = $1 AND domain_id = $2`, id, domainID)

		return err
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

func (s *ScorecardStore) UpsertSnapshot(ctx context.Context, domainID int64, scorecardID int32) (int64, error) {
	db, err := s.pm.Primary()
	if err != nil {
		return 0, ParseError(err)
	}

	current, err := snapshotRead(ctx, db, func(q querier) (*store.ScorecardView, error) {
		return readOne(ctx, q, domainID, scorecardID, nil)
	})
	if err != nil {
		return 0, err
	}

	const sql = `INSERT INTO audit.scorecard_snapshot (scorecard_id, domain_id, hash, content)
		VALUES (@scorecard_id, @domain_id, @hash, @content)
		ON CONFLICT (scorecard_id, hash) DO UPDATE SET hash = excluded.hash
		RETURNING id`

	var id int64

	err = db.QueryRow(ctx, sql, pgx.NamedArgs{
		"scorecard_id": scorecardID,
		"domain_id":    domainID,
		"hash":         current.ContentHash(),
		"content":      string(current.SnapshotContent()),
	}).Scan(&id)
	if err != nil {
		return 0, ParseError(err)
	}

	return id, nil
}

// lockScorecard locks the scorecard row for the rest of the transaction and reports editable.
func lockScorecard(ctx context.Context, tx querier, domainID int64, id int32, rbac *store.RBAC, access int) (bool, error) {
	q := psql.Select("s.editable").From("audit.scorecard s").
		Where("s.id = ? AND s.domain_id = ?", id, domainID).
		Suffix("FOR UPDATE OF s")
	if rbac != nil {
		q = q.Where(rbacFilter(rbac, access))
	}

	sql, args, err := q.ToSql()
	if err != nil {
		return false, err
	}

	var editable bool
	if err := tx.QueryRow(ctx, sql, args...).Scan(&editable); err != nil {
		if stderrors.Is(err, pgx.ErrNoRows) {
			return false, errScorecardNotFound
		}

		return false, err
	}

	return editable, nil
}

// rbacFilter keeps the rows whose ACL grants one of rbac.Roles the access bit.
func rbacFilter(rbac *store.RBAC, access int) squirrel.Sqlizer {
	return squirrel.Expr(`EXISTS (SELECT 1 FROM audit.scorecard_acl a
		WHERE a.object = s.id AND a.dc = s.domain_id AND a.subject = ANY(?::bigint[]) AND a.access & ?::int = ?::int)`,
		rbac.Roles, access, access)
}

// parseScorecardError gives the duplicate form name its own id, the rest goes through ParseError.
func parseScorecardError(err error) error {
	var pgErr *pgconn.PgError
	if stderrors.As(err, &pgErr) && pgErr.ConstraintName == scorecardDuplicateNameConstraint {
		return errors.New("scorecard with this name already exists",
			errors.WithCode(errors.Code(ParseError(err))), errors.WithID("audit.scorecard.name_exists"), errors.WithCause(err))
	}

	return ParseError(err)
}

func readOne(ctx context.Context, db querier, domainID int64, id int32, rbac *store.RBAC) (*store.ScorecardView, error) {
	items, err := search(ctx, db, domainID,
		&store.ScorecardSearch{IDs: []int32{id}, Fields: scorecardFields}, rbac, util.NewPaging(1, 1))
	if err != nil {
		return nil, err
	}

	if len(items) == 0 {
		return nil, errScorecardNotFound
	}

	return items[0], nil
}

// search fetches paging.Limit() rows (one more than a page) and the children Fields ask for.
func search(
	ctx context.Context, db querier, domainID int64, params *store.ScorecardSearch, rbac *store.RBAC, paging util.Paging,
) ([]*store.ScorecardView, error) {
	fields, err := util.ParseFields(params.Fields, scorecardFields, scorecardDefaultFields)
	if err != nil {
		return nil, err
	}

	sql, args, err := buildSearch(domainID, params, rbac, paging)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, ParseError(err)
	}

	items, err := pgx.CollectRows(rows, scanScorecard)
	if err != nil {
		return nil, ParseError(err)
	}

	if len(items) == 0 {
		return items, nil
	}

	if err := loadChildren(ctx, db, items, fields); err != nil {
		return nil, ParseError(err)
	}

	return items, nil
}

func buildSearch(domainID int64, p *store.ScorecardSearch, rbac *store.RBAC, paging util.Paging) (string, []any, error) {
	q := psql.Select(
		"s.id", "s.domain_id", "s.name", "coalesce(s.description, '')", "s.enabled", "s.archive", "s.editable",
		"s.interaction_type", "s.created_at", "s.created_by", "coalesce(uc.name, uc.username)",
		"s.updated_at", "coalesce(s.updated_by, 0)", "coalesce(uu.name, uu.username)",
	).
		From("audit.scorecard s").
		LeftJoin("directory.wbt_user uc ON uc.id = s.created_by").
		LeftJoin("directory.wbt_user uu ON uu.id = s.updated_by").
		Where("s.domain_id = ?", domainID).
		Limit(paging.Limit()).
		Offset(paging.Offset())

	if rbac != nil {
		q = q.Where(rbacFilter(rbac, store.AccessRead))
	}

	if p.Q != "" {
		like := likePattern(p.Q)
		q = q.Where("(s.name ILIKE ? OR s.description ILIKE ?)", like, like)
	}

	if len(p.IDs) > 0 {
		q = q.Where("s.id = ANY(?::int[])", p.IDs)
	}

	if len(p.TeamIDs) > 0 {
		q = q.Where(`EXISTS (`+liveTeam+`AND st.team_id = ANY(?::bigint[]))`, p.TeamIDs)
	}

	if p.Enabled != nil {
		q = q.Where("s.enabled = ?", *p.Enabled)
	}

	if p.Editable != nil {
		q = q.Where("s.editable = ?", *p.Editable)
	}

	if p.Archive != nil {
		q = q.Where("s.archive = ?", *p.Archive)
	}

	if len(p.InteractionTypes) > 0 {
		q = q.Where("s.interaction_type && ?::smallint[]", p.InteractionTypes)
	}

	if p.Question != "" {
		q = q.Where(`EXISTS (SELECT 1 FROM audit.scorecard_criterion c
			WHERE c.scorecard_id = s.id AND c.question ILIKE ?)`, likePattern(p.Question))
	}

	// Engine cc_audit_form GetAllPage team_filter: forms without teams or with a team of the user's agent.
	const userTeam = `EXISTS (` + liveTeam + `AND EXISTS (SELECT 1 FROM call_center.cc_agent ag
		WHERE ag.team_id = st.team_id AND ag.domain_id = st.domain_id AND ag.user_id = ?))`
	if p.TeamUserID != 0 {
		q = q.Where(`(NOT EXISTS (`+liveTeam+`) OR `+userTeam+`)`, p.TeamUserID)
	}

	switch {
	case p.Sort == "" && p.TeamUserID != 0:
		q = q.OrderByClause(`CASE WHEN `+userTeam+` THEN 0 ELSE 1 END, s.name`, p.TeamUserID)
	default:
		sort := p.Sort
		if sort == "" {
			sort = defaultScorecardSort
		}

		orderBy, err := util.ParseSort(sort, scorecardSortable)
		if err != nil {
			return "", nil, err
		}

		q = q.OrderBy(orderBy...)
	}

	// The id tiebreaker keeps pages stable when the sort keys repeat.
	return q.OrderBy("s.id").ToSql()
}

func likePattern(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`, `*`, `%`).Replace(s)
}

func scanScorecard(row pgx.CollectableRow) (*store.ScorecardView, error) {
	var (
		out                    store.ScorecardView
		creatorName, updaterNm *string
	)

	err := row.Scan(&out.ID, &out.DomainID, &out.Name, &out.Description, &out.Enabled, &out.Archive, &out.Editable,
		&out.InteractionTypes, &out.CreatedAt, &out.CreatedBy, &creatorName, &out.UpdatedAt, &out.UpdatedBy, &updaterNm)
	if err != nil {
		return nil, err
	}

	out.Creator = lookup(out.CreatedBy, creatorName)
	out.Updater = lookup(out.UpdatedBy, updaterNm)

	return &out, nil
}

func lookup(id int64, name *string) *store.Lookup {
	if id == 0 {
		return nil
	}

	out := &store.Lookup{ID: id}
	if name != nil {
		out.Name = *name
	}

	return out
}

// loadChildren fills teams, groups and criteria of items with one query per level.
func loadChildren(ctx context.Context, db querier, items []*store.ScorecardView, fields []string) error {
	byID := make(map[int32]*store.ScorecardView, len(items))
	ids := make([]int32, 0, len(items))

	for _, it := range items {
		byID[it.ID] = it
		ids = append(ids, it.ID)
	}

	args := pgx.NamedArgs{"ids": ids}

	if slices.Contains(fields, fieldTeams) {
		// A team deleted from call_center is left out: otherwise its id comes back on the next save and
		// replaceTeams refuses it as unknown, so the form could not be saved any more.
		rows, err := db.Query(ctx, `SELECT st.scorecard_id, st.team_id, t.name
			FROM audit.scorecard_team st
			JOIN call_center.cc_team t ON t.id = st.team_id AND t.domain_id = st.domain_id
			WHERE st.scorecard_id = ANY(@ids)
			ORDER BY t.name, st.team_id`, args)
		if err != nil {
			return err
		}

		var (
			owner int32
			team  store.Lookup
		)

		_, err = pgx.ForEachRow(rows, []any{&owner, &team.ID, &team.Name}, func() error {
			byID[owner].TeamIDs = append(byID[owner].TeamIDs, team.ID)
			byID[owner].Teams = append(byID[owner].Teams, team)

			return nil
		})
		if err != nil {
			return err
		}
	}

	if slices.Contains(fields, fieldGroups) {
		rows, err := db.Query(ctx, `SELECT scorecard_id, id, name, position
			FROM audit.scorecard_criteria_group
			WHERE scorecard_id = ANY(@ids)
			ORDER BY scorecard_id, position`, args)
		if err != nil {
			return err
		}

		var (
			owner int32
			g     model.CriteriaGroup
		)

		_, err = pgx.ForEachRow(rows, []any{&owner, &g.ID, &g.Name, &g.Position}, func() error {
			byID[owner].Groups = append(byID[owner].Groups, g)

			return nil
		})
		if err != nil {
			return err
		}
	}

	if slices.Contains(fields, fieldCriteria) {
		return loadCriteria(ctx, db, byID, args)
	}

	return nil
}

func loadCriteria(ctx context.Context, db querier, byID map[int32]*store.ScorecardView, args pgx.NamedArgs) error {
	rows, err := db.Query(ctx, `SELECT scorecard_id, id, group_id, position, type, question, coalesce(description, ''),
			required, critical_violation, allow_na, coalesce(min_score, 0), coalesce(max_score, 0), weight,
			require_comment, comment_threshold
		FROM audit.scorecard_criterion
		WHERE scorecard_id = ANY(@ids)
		ORDER BY scorecard_id, position`, args)
	if err != nil {
		return err
	}

	var (
		owner     int32
		c         model.Criterion
		groupID   *uuid.UUID
		threshold *int16
	)

	_, err = pgx.ForEachRow(rows, []any{
		&owner, &c.ID, &groupID, &c.Position, &c.Type, &c.Question, &c.Description,
		&c.Required, &c.CriticalViolation, &c.AllowNA, &c.Min, &c.Max, &c.Weight,
		&c.RequireComment, &threshold,
	}, func() error {
		c.GroupID = uuid.Nil
		if groupID != nil {
			c.GroupID = *groupID
		}

		c.CommentThreshold = nil

		if threshold != nil {
			v := *threshold
			c.CommentThreshold = &v
		}

		byID[owner].Criteria = append(byID[owner].Criteria, c)

		return nil
	})
	if err != nil {
		return err
	}

	// Criteria by id, for the options below; taken after the appends so the pointers stay valid.
	at := make(map[uuid.UUID]*model.Criterion)

	for _, it := range byID {
		for i := range it.Criteria {
			at[it.Criteria[i].ID] = &it.Criteria[i]
		}
	}

	rows, err = db.Query(ctx, `SELECT o.criterion_id, o.id, o.position, o.name, o.score, o.require_comment
		FROM audit.scorecard_criterion_option o
		JOIN audit.scorecard_criterion c ON c.id = o.criterion_id
		WHERE c.scorecard_id = ANY(@ids)
		ORDER BY o.criterion_id, o.position`, args)
	if err != nil {
		return err
	}

	var (
		criterionID uuid.UUID
		o           model.Option
	)

	_, err = pgx.ForEachRow(rows, []any{&criterionID, &o.ID, &o.Position, &o.Name, &o.Score, &o.RequireComment}, func() error {
		at[criterionID].Options = append(at[criterionID].Options, o)

		return nil
	})

	return err
}

// writeChildren makes the teams, groups, criteria and options of scorecard id exactly those of in, rejecting any id that belongs to another scorecard.
func writeChildren(ctx context.Context, tx querier, domainID int64, id int32, in *model.Scorecard) error {
	if err := replaceTeams(ctx, tx, domainID, id, in.TeamIDs); err != nil {
		return err
	}

	groupIDs := make([]uuid.UUID, 0, len(in.Groups))
	for _, g := range in.Groups {
		groupIDs = append(groupIDs, g.ID)
	}

	criterionIDs := make([]uuid.UUID, 0, len(in.Criteria))
	for _, c := range in.Criteria {
		criterionIDs = append(criterionIDs, c.ID)
	}

	batch := &pgx.Batch{}
	batch.Queue(`DELETE FROM audit.scorecard_criterion WHERE scorecard_id = $1 AND NOT id = ANY($2::uuid[])`, id, criterionIDs)
	batch.Queue(`DELETE FROM audit.scorecard_criteria_group WHERE scorecard_id = $1 AND NOT id = ANY($2::uuid[])`, id, groupIDs)
	batch.Queue(`DELETE FROM audit.scorecard_criterion_option o USING audit.scorecard_criterion c
		WHERE c.id = o.criterion_id AND c.scorecard_id = $1`, id)

	if err := execBatch(ctx, tx, batch, false); err != nil {
		return err
	}

	// Rows are upserted one by one; the unique group name is deferred to the commit, so names may move between groups.
	batch = &pgx.Batch{}
	for _, g := range in.Groups {
		batch.Queue(`INSERT INTO audit.scorecard_criteria_group AS g (id, scorecard_id, name, position)
			VALUES (@id, @scorecard_id, @name, @position)
			ON CONFLICT (id) DO UPDATE SET name = excluded.name, position = excluded.position
			WHERE g.scorecard_id = excluded.scorecard_id`,
			pgx.NamedArgs{"id": g.ID, "scorecard_id": id, "name": g.Name, "position": g.Position})
	}

	for _, c := range in.Criteria {
		var groupID *uuid.UUID
		if c.GroupID != uuid.Nil {
			groupID = &c.GroupID
		}

		var minScore, maxScore *int16
		if c.Type == model.CriterionTypeScore {
			minScore, maxScore = &c.Min, &c.Max
		}

		batch.Queue(`INSERT INTO audit.scorecard_criterion AS c (id, scorecard_id, group_id, position, type, question,
				description, required, critical_violation, allow_na, min_score, max_score, weight, require_comment,
				comment_threshold)
			VALUES (@id, @scorecard_id, @group_id, @position, @type, @question, @description, @required,
				@critical_violation, @allow_na, @min_score, @max_score, @weight, @require_comment, @comment_threshold)
			ON CONFLICT (id) DO UPDATE SET group_id = excluded.group_id, position = excluded.position,
				type = excluded.type, question = excluded.question, description = excluded.description,
				required = excluded.required, critical_violation = excluded.critical_violation,
				allow_na = excluded.allow_na, min_score = excluded.min_score, max_score = excluded.max_score,
				weight = excluded.weight, require_comment = excluded.require_comment,
				comment_threshold = excluded.comment_threshold
			WHERE c.scorecard_id = excluded.scorecard_id`,
			pgx.NamedArgs{
				"id": c.ID, "scorecard_id": id, "group_id": groupID, "position": c.Position, "type": c.Type,
				"question": c.Question, "description": nullIfEmpty(c.Description), "required": c.Required,
				"critical_violation": c.CriticalViolation, "allow_na": c.AllowNA, "min_score": minScore,
				"max_score": maxScore, "weight": c.Weight, "require_comment": c.RequireComment,
				"comment_threshold": c.CommentThreshold,
			})

		for _, o := range c.Options {
			batch.Queue(`INSERT INTO audit.scorecard_criterion_option (id, criterion_id, position, name, score, require_comment)
				VALUES (@id, @criterion_id, @position, @name, @score, @require_comment)
				ON CONFLICT (id) DO NOTHING`,
				pgx.NamedArgs{
					"id": o.ID, "criterion_id": c.ID, "position": o.Position, "name": o.Name,
					"score": o.Score, "require_comment": o.RequireComment,
				})
		}
	}

	return execBatch(ctx, tx, batch, true)
}

// execBatch runs the batch; with eachRow every statement must touch exactly one row,
// a statement that touched none hit an id of another scorecard.
func execBatch(ctx context.Context, tx querier, batch *pgx.Batch, eachRow bool) error {
	br := tx.SendBatch(ctx, batch)

	for range batch.Len() {
		tag, err := br.Exec()
		if err != nil {
			_ = br.Close()

			return ParseError(err)
		}

		if eachRow && tag.RowsAffected() != 1 {
			_ = br.Close()

			return errScorecardForeignID
		}
	}

	return ParseError(br.Close())
}

// replaceTeams binds the scorecard to exactly teamIDs, each a team of the same domain.
func replaceTeams(ctx context.Context, tx querier, domainID int64, id int32, teamIDs []int64) error {
	if _, err := tx.Exec(ctx, `DELETE FROM audit.scorecard_team WHERE scorecard_id = $1`, id); err != nil {
		return ParseError(err)
	}

	if len(teamIDs) == 0 {
		return nil
	}

	tag, err := tx.Exec(ctx, `INSERT INTO audit.scorecard_team (scorecard_id, team_id, domain_id)
		SELECT @id, t.id, t.domain_id FROM call_center.cc_team t
		WHERE t.id = ANY(@team_ids) AND t.domain_id = @domain_id`,
		pgx.NamedArgs{"id": id, "team_ids": teamIDs, "domain_id": domainID})
	if err != nil {
		return ParseError(err)
	}

	if tag.RowsAffected() != int64(len(slices.Compact(slices.Sorted(slices.Values(teamIDs))))) {
		return errScorecardTeamUnknown
	}

	return nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

func nullIfZero(v int64) *int64 {
	if v == 0 {
		return nil
	}

	return &v
}
