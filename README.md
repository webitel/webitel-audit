# webitel-audit

Audit service of the Webitel platform. Built on the shared go-kit
(`appconfig`, `infra/discovery`, `infra/health`, `infra/otel`, `infra/pgw`, `infra/transport`) and wired with
[fx](https://github.com/uber-go/fx).

## Run

```sh
go run . server          # run the service
go run . migrate         # apply pending migrations to the audit schema
```

Configured only through environment variables or a config file
(`--config_file path`, or `CONFIG_FILE=path`, YAML or JSON). Copy
[`.env.example`](.env.example) to `.env` and export it before running
locally (`.env.otel.example` for OpenTelemetry). The configuration is
validated on start; an invalid one stops the service.

| Env | Default | Description |
|---|---|---|
| `SERVICE_ADDR` | `127.0.0.1:8080` | gRPC listen address |
| `SERVICE_CONN_VERIFY_CERTS` | `true` | gRPC TLS; needs `SERVICE_CONN_CA`, `_CERT` and `_KEY`. Set `false` locally |
| `SERVICE_CONN_MTLS` | `true` | require client certificates (with verified certs) |
| `CONSUL_ADDR` | `localhost:8500` | Consul agent the service registers in |
| `HEALTH_ADDR` | not set | listen address of `/livez`, `/readyz`, `/healthz`; unset disables the probes |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_JSON` | `false` | JSON console output |
| `LOG_CONSOLE` | `true` | write logs to stdout |
| `LOG_FILE` | | write logs to a file |
| `LOG_OTEL` | `false` | add the OpenTelemetry log bridge |
| `POSTGRES_DSN` | not set | PostgreSQL connection string, required by `server` and `migrate` |
| `POSTGRES_MAX_OPEN_CONNS` | `0` | max connections of the server pool; `0` keeps the pgx default |

## Database

Tables live in the `audit` schema of the shared Webitel database. Migrations
are embedded in the binary and applied only by `migrate` (no `down`).

The server does not check the schema version on start.

## Tests

```sh
go test ./...
```

Mocks are generated with MockGen into `gen/mocks`.

## Generated API code (`gen/go/audit/v1`)

```sh
buf generate /path/to/local/audit --template buf.gen.yaml
```

HTTP paths: `/v1/audit/scorecards[/{id}]`.

## Differences from engine AuditFormService

- Package `webitel.audit.v1`, service `ScorecardService`, RPCs `CreateScorecard`, `SearchScorecards`,
  `ReadScorecard`, `UpdateScorecard`, `PatchScorecard`, `DeleteScorecard`; each has a
  `<Rpc>Response` (see below).
- `AuditForm` is `Scorecard`; `Question` is `Criterion` (JSON `questions` is `criteria`, `question` is `text`,
  Search filter `question` is `criterion_text`); enum `AuditQuestionType` is `CriterionType`
  (`CRITERION_TYPE_SCORE|OPTION|YES`); enum values of `InteractionType` are `INTERACTION_TYPE_*`.
- Paths are `/v1/audit/scorecards`, not `/call_center/audit/forms`.
- Search: the default fields do not include `criteria` and `criteria_groups`; ask for them in `fields`.
- Search: `enabled`, `editable` and `archive` are optional filters; unset means no filter, `false` now filters.
- Search: `fields=domain_id` is rejected (400).
- PUT replaces every field, `archive` included: a PUT without `archive` resets it to `false` (engine PUT never
  touched `archive`, it kept the stored value).
- A record the caller has no ACL access to returns 404, not 403.
- New error codes: a duplicate form name is 409 `audit.scorecard.name_exists` (engine: 400
  `store.sql_audit_form.save.valid.name`); a team that does not exist in the domain is 400
  `audit.scorecard.team_unknown`; changing the structure of a form that has evaluations, or deleting it, is
  403 `audit.scorecard.not_editable` (engine `UpdateAuditForm` checked nothing).
- New validation on write (Confluence «Е2. Scorecards»): score range -15..15 with `max >= 0` (US_2.7), weight
  1..10 with at most 2 decimals (US_2.10), comment threshold for a Score criterion that requires a comment
  (US_2.11), at least one interaction type (US_2.4), and an enabled form needs a required Score/Option
  criterion with a maximum above 0 (US_2.16). Engine checked only name and description length, `max != 0` and
  `min <= max` for Score and the presence of options.
- A team deleted from `call_center` is left out of the form on read and in the `team_id` / `team_filter`
  filters, so the form can still be saved (engine kept the dead id).
- A new form gets the default role access of the `cc_audit_form` class (the trigger of `scorecard_acl` uses the
  right class; engine passed the wrong one and never gave roles any default access).
- PATCH: the mask field is `x_json_mask` (was `fields`); the frontend uses PUT only.
- `interaction_type` is required on write (at least one).
- Ids are UUIDs issued by the server. Send an existing row's uuid back on update. For a NEW row leave the id
  empty or use any short key that is not a uuid (up to 64 characters, e.g. `"g1"`, `"q1"`): the server issues the
  uuid and the response carries it. A new group needs a key only when criteria of the same request refer to it;
  they put the same key in `group_id`, the server swaps it for the issued uuid. Keys of groups must be unique in
  the request. After a save, use the uuids from the response: a key sent again for an existing row is a new row
  (a fresh uuid every time, which also counts as a structural change for a form with evaluations).
  `position` from the client is ignored: the array order wins.
- Every RPC has its own response: `CreateScorecardResponse`, `ReadScorecardResponse`, `UpdateScorecardResponse`,
  `PatchScorecardResponse` and `DeleteScorecardResponse` carry the entity in `scorecard`, so JSON is
  `{"scorecard": {...}}`, not the bare entity. `buf lint` (STANDARD) is clean except `PACKAGE_DIRECTORY_MATCH`
  (package `webitel.audit.v1` lives in the module root, like the other protos).

## Notes for DT-41 (engine -> audit migration)

- The trigger `scorecard_set_rbac_acl` fires on every INSERT into `audit.scorecard` and writes the owner row and
  the role defaults; copying `cc_audit_form_acl` afterwards then conflicts with them. Disable the trigger for the
  copy, or insert the ACL rows with `ON CONFLICT DO NOTHING`.
- `audit.scorecard.id` is `GENERATED BY DEFAULT AS IDENTITY`: after inserting the engine ids explicitly, `setval`
  the sequence to the max id, or the next create collides. Forms created through this API before the migration
  (while the service is tested next to engine) already took ids from 1 and may collide with the engine ids:
  copy into an empty `audit.scorecard`, or renumber/clear the test forms first, otherwise the copy fails on the
  primary key or skips rows.
- `created_by` is `NOT NULL` (a plain user id, no FK): engine forms whose author was deleted (NULL) need a
  substitute user.
- Run `model.Scorecard.Normalize` on the migrated criteria before writing them (Say "Yes" loses Required,
  critical_violation only on Say "Yes" and then requires a comment, weight of Say "Yes" is 1, etc.).
- Old evaluations have no snapshot: build it from the current form (`UpsertSnapshot`).
- Engine did not limit score scales, so the DB has no `-15..15` CHECK on `min_score`, `max_score` and option
  `score`: out-of-range forms can be copied as is. `model.Validate` still enforces the range, so editing such a
  form through the API fails until its scale is fixed.
- Forms that engine accepted but `model.Validate` refuses, so any save of them through the API fails until the
  form is fixed: a name shorter than 3 characters (engine counted bytes, so a 2-letter Cyrillic name such as
  "КЦ" passed there), an empty criterion text, a criterion text over 2000 characters. List them before the
  migration.
- Copy only the teams that still exist: `audit.scorecard_team` rows for a team missing in `call_center.cc_team`
  are invisible on read (see above), so skip them with a JOIN on `cc_team` instead of copying dead ids.
- Confluence «Е2. Scorecards», AC_02.04.04: every existing form gets `interaction_type = {1}` (Call).
- Confluence «Е2. Scorecards», US_2.16 (an enabled form needs a required Score/Option criterion with max > 0)
  is new: engine has no such check. Normalize clears `required` on Say "Yes" (US_2.15), so an engine form whose
  only required criterion was Say "Yes" fails with 400 `Add at least one required criterion with a maximum
  score above 0` on any save. Chosen way out: the user adds a required Score/Option criterion, or saves a copy and
  archives the old form. To archive it, send `enabled=false` and `archive=true` together: a disabled form is
  not checked against US_2.16, while archiving an enabled one still fails. Before the migration, count such forms
  (enabled, no required Score/Option criterion with max > 0) to know how many users meet this.
