package store

//go:generate go tool mockgen -source=scorecard.go -destination=../../gen/mocks/scorecard_store_mock.go -package=mocks

import (
	"context"

	"github.com/webitel/webitel-audit/internal/model"
)

// Object access bits of an ACL row; the same values as engine auth_manager.PermissionAccess.
const (
	AccessDelete = 1
	AccessUpdate = 2
	AccessRead   = 4
	AccessCreate = 8
)

// RBAC limits a call to scorecards on which the caller's roles hold the method's own access bit (scorecard_acl); nil means no object-level check.
type RBAC struct {
	Roles []int64
}

type Lookup struct {
	ID   int64
	Name string
}

type ScorecardView struct {
	model.Scorecard

	Creator *Lookup
	Updater *Lookup
	Teams   []Lookup
}

// ScorecardSearch selects a page of scorecards; zero values disable a filter.
type ScorecardSearch struct {
	Page int
	Size int
	// Sort is "+field,-field"; empty sorts by -created_at.
	Sort string
	// Fields picks the parts to load; scalar columns always come back, "teams", "groups" and
	// "criteria" are loaded only when listed. Empty loads everything except groups and criteria.
	Fields           []string
	Q                string
	IDs              []int32
	TeamIDs          []int64
	Enabled          *bool
	Editable         *bool
	Archive          *bool
	InteractionTypes []model.InteractionType
	// Question matches a form that has a criterion with this text in its question.
	Question   string
	TeamFilter bool
	TeamUserID int64
}

type ScorecardStore interface {
	Create(ctx context.Context, domainID int64, in *model.Scorecard) (*ScorecardView, error)
	Read(ctx context.Context, domainID int64, id int32, rbac *RBAC) (*ScorecardView, error)
	Search(ctx context.Context, domainID int64, params *ScorecardSearch, rbac *RBAC) ([]*ScorecardView, bool, error)
	Patch(
		ctx context.Context, domainID int64, id int32, rbac *RBAC, mutate func(sc *model.Scorecard, editable bool) error,
	) (*ScorecardView, error)
	Delete(ctx context.Context, domainID int64, id int32, rbac *RBAC) (*ScorecardView, error)
	UpsertSnapshot(ctx context.Context, domainID int64, scorecardID int32) (int64, error)
}
