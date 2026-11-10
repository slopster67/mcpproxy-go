# Debloat log

This file records the removal of features, modules, and supporting material from
MCPProxy. Append future changes with their scope, affected paths, and commit IDs.
The initial inventory below describes the actual branch diff, rather than relying
only on commit messages.

## Initial baseline — 2026-10-17

- Original: `main` at `87db6d7a27e7506a45307aaee6127d3f0d7e6aee`.
- Stripped: `stripped` at `960794d84c12e3ba0225d3aa3e00f28412e933cc`.
- Comparison: `git diff main stripped` (the original is an ancestor of stripped).
- This inventory and its measurements exclude the addition of `DEBLOAT.md` itself.

### Size

The headline LOC metric counts source and build-script code using **cloc 2.06**,
excluding blank lines and comments. It includes tests and generated source in both
snapshots. It excludes documentation, JSON/YAML data, lockfiles, SVG/XML assets,
WiX packaging, patches, and TOML configuration. This measures repository code,
including development tooling, rather than just production application code.

| Code | Original LOC | Stripped LOC | Net reduction |
| --- | ---: | ---: | ---: |
| Go | 522,547 | 118,435 | 404,112 |
| TypeScript | 53,520 | 45,986 | 7,534 |
| Vue components | 28,259 | 17,629 | 10,630 |
| Swift | 51,325 | 0 | 51,325 |
| Other source and build scripts | 17,984 | 2,178 | 15,806 |
| **Total** | **673,635** | **184,228** | **489,407** |

**27.35% of the original LOC remains; 72.65% was removed on a net basis.**
The remaining percentage is the ratio of snapshot sizes, not a claim that every
remaining line is unchanged from the original.

For a broader repository-content metric, cloc's total across all recognized
languages (including Markdown, JSON, YAML, and other non-source material) is
917,310 → 238,786 nonblank, noncomment lines: a reduction of 678,524, with 26.03%
remaining. Do not mix that metric with the source/build LOC above.

Tracked files fell from 4,955 to 1,232: 3,732 files deleted and 9 added.
Git reports 1,562 inserted and 889,817 deleted physical lines across 3,902 changed
files. Those diff counts include comments, blank lines, documentation, and data;
they are different from cloc's code-only snapshot totals.

Reproduce with cloc 2.06 from the repository root (use the pinned commits above
when the branches advance). `--skip-uniqueness` counts duplicate tracked files
individually, and `--git` excludes untracked build output and installed packages:

```sh
langs='Go,TypeScript,Swift,Vuejs Component,Bourne Shell,HTML,Python,JavaScript,CSS,PowerShell,make,Dockerfile,DOS Batch,Bourne Again Shell'
cloc --git 87db6d7a27e7506a45307aaee6127d3f0d7e6aee --skip-uniqueness --include-lang="$langs"
cloc --git 960794d84c12e3ba0225d3aa3e00f28412e933cc --skip-uniqueness --include-lang="$langs"
# Omit --include-lang to reproduce the broader repository-content totals.
```

### Removed product features and modules

1. **Docker isolation for upstream servers.** Removed the managed container
   launch/isolation path, associated configuration, naming helpers, diagnostics,
   and isolation projections. Paths include `internal/dockernaming/`,
   `internal/upstream/core/{docker,connection_docker,isolation}.go`, and Docker
   isolation fields in `internal/config/`. Explicit user-supplied Docker commands
   remain possible; this removal concerns MCPProxy-managed isolation.
   Commits: `6f0cd38c`, `9a492eee`.

2. **Native sandbox isolation.** Deleted `internal/sandbox/`, sandbox execution
   and resource-limit wiring, the `sandbox-exec` CLI command, and isolation
   configuration/resolution. Commits: `50f3c7a8`, `9a492eee`.

3. **Security scanner system and scan-based approval.** Removed the plugin engine,
   Docker scanners/images, SARIF processing, package-source fetching/staging,
   offline TPA detection/signature bundle, scan jobs/history/storage, automatic
   baseline scans, deep scans, and the scanner approval gate. Deleted
   `internal/security/scanner/`, `internal/security/detect/`, `docker/scanners/`,
   `internal/httpapi/security_scanner.go`, and `internal/storage/scanner.go`.
   Frontend scanner settings, findings panels, scan reports, and scanner badges
   were removed; the trust-mode UI now exposes `auto` and `manual`.
   Commits: `72a3c4da`, `7aacbea6`, `91547edf`, `942cd83b`.

4. **Sensitive-data scanning and output sanitisation.** Deleted the remaining
   `internal/security/` detectors, entropy/Luhn checks, masking logic, and
   `internal/server/output_sanitisation.go`, with their runtime/config wiring.
   Existing credential redaction elsewhere is a separate retained capability.
   Commit: `91547edf`.

5. **Server edition / Teams / multi-user SSO.** Deleted
   `internal/serveredition/`, edition-specific configuration/build-tag accessors,
   session-principal middleware, server-edition CLI registration, and API wiring.
   Removed login, per-user server/activity/diagnostics/token pages, admin
   dashboard/user/server pages, session route guards, and edition settings from
   the frontend. Commits: `25e4a253`, `cd79bb1f`, `98ca540a`, `942cd83b`.

6. **Auth broker and IdP token storage.** Removed brokered per-user credentials,
   the broker credential store/OAuth connector, IdP-related storage/wiring, broker
   configuration, and the broker credential CLI. Upstream-server OAuth remains.
   Commits: `93d669e4`, `98ca540a`.

7. **Automatic updates and version checks.** Deleted `internal/updatecheck/`,
   update-failure plumbing, update status/CLI integration, and the UI update
   banner. Commits: `cb1dcd5c`, `526cd071`, `942cd83b`.

8. **Registry/catalog discovery and management.** Deleted
   `internal/registries/`, registry/catalog CLI commands, registry MCP operations,
   catalog HTTP handlers, popularity support, and registry configuration/contracts.
   Removed catalog search and catalog-source settings from the UI.
   Commits: `15f7b942`, `1b594485`, `942cd83b`.

9. **Importing servers from other clients.** Deleted `internal/configimport/`,
   import HTTP handlers, format-specific import types, and both import UI panels.
   Add Server now offers the manual form; its catalog and paste helpers were also
   deleted. Commits: `3c156e71`, `50e0d723`, `942cd83b`, `960794d8`.

10. **Writing configuration into other clients.** Removed connect/disconnect
    writers, preview/backup/undo/token helpers, connect CLI/API endpoints, and the
    client-connection UI. `internal/connect/clients.go` remains as client metadata;
    client identities, bindings, profiles, and tokens remain separate features.
    Commits: `c35bfea7`, `a31875b8`, `942cd83b`.

11. **Desktop tray application.** Deleted `native/` (Swift/macOS application),
    `cmd/mcpproxy-tray/`, and `internal/tray/`, plus tray configuration/feature flags
    and related startup integration. Commits: `7317e1f0`, `98ca540a`.

12. **Interactive terminal UI.** Deleted `internal/tui/` and the `tui` command;
    removed Bubble Tea/Lip Gloss dependencies. Commit: `818ecfbd`.

13. **Telemetry, feedback, and onboarding UI.** Deleted `internal/telemetry/`,
    telemetry CLI/API/config/startup/event hooks, machine-identity collection,
    feedback submission, telemetry banner, feedback page, and onboarding
    wizard/store. Local activity/usage reporting and observability code remain.
    Commits: `475c6d53`, `930d4bad`, `be9fe031`, `942cd83b`.

14. **Token-savings presentation.** Removed the token pie chart, usage summary
    strip, and savings statistics from Home, Usage, and system state. The Usage
    page and underlying activity/usage functionality remain.
    Commit: `be34362a`.

15. **Windows and other platform-specific support.** Removed Windows process,
    job-object, named-pipe/socket/listener, shell, and launcher implementations,
    `internal/winjob/`, WiX installers, and other non-Linux/non-Darwin platform
    implementations. The intended platform scope is now Linux/macOS; no platform
    build verification is claimed by this inventory. Commit: `e5fbaafd`.

### Removed tests, tooling, and repository material

- **Go tests and test support:** all 1,747 tracked `*_test.go` files from `main`
  are gone, along with fixtures, golden files, test-only helpers,
  `internal/testutil/`, and the top-level `e2e/`, `test/`, and `tests/` trees.
  **311 frontend `.test.ts`/`.spec.ts` files remain**, so the repository is not
  test-free. Commits: `234735e8`, `dda2f2bd`, `3414a16a`, `162e3c51`.
- **Benchmark/evaluation harness:** deleted `bench/`, its command-line tools,
  corpora/replay/agent-loop/reporting code, and `cmd/scan-eval/` and
  `cmd/scope-latency-compare/`. Commit: `e0f32c28` and tooling cleanup commits.
- **QA/development executables and hooks:** deleted `cmd/qapilot/`,
  `cmd/mcpfixture/`, `cmd/release-gate/`, `cmd/tpa-redteam/`,
  `cmd/populate-test-data/`, `internal/qahooks/`, `internal/gatereport/`, and
  `internal/qapilot/`. Commits: `0f84018b`, `7efa5421`, `162e3c51`.
- **Code-generation helpers:** deleted `cmd/generate-types/` and
  `cmd/oas-nullable/`; existing generated source/contracts are not necessarily
  removed with their generators.
- **Scripts, packaging, and contribution support:** deleted `scripts/`,
  `contrib/`, `.pre-commit-config.yaml`, `cliff.toml`, `glama.json`, and
  `server.json`. Removed E2E Makefile targets. Commits: `e030fdb2`, `1ef3fadc`.
- **Specification and documentation machinery:** deleted `.specify/`, `specs/`,
  `oas/`, the Docusaurus `website/`, `AGENTS.md`, `CLAUDE.md`, `AUTOUPDATE.md`,
  `CHANGELOG.md`, `CONTRIBUTING.md`, `ROADMAP.md`, `roadmap.yaml`, and
  `roadmap.archive.yaml`. `docs/` and `README.md` remain.
  Commits: `e030fdb2`, `1ef3fadc`.
- **Dependency cleanup:** removed direct dependencies for the tray, TUI,
  auto-updater, machine ID, desktop notifications, JWT/server-edition support,
  Windows I/O, and Go testing, along with associated indirect dependencies.
  `frontend/package.json` is unchanged between these snapshots.

CI workflows were already removed on `main` by `87db6d7a`; they are not an
additional removal in the `main` → `stripped` diff.

### Retained functionality and follow-up observations

The core proxy/CLI and embedded web UI remain, including upstream connections
and OAuth, discovery/search/routing, code execution and stored scripts,
profiles/client bindings/tokens, manual quarantine/tool review, secrets,
activity/usage, diagnostics, storage, and local observability.

This is a removal inventory, not a build or runtime validation report. Some
comments, docs, tests, configuration stubs, and Makefile targets still reference
deleted features. Examples include scanner/trust-mode comments in
`internal/config/config.go`, removed-feature claims in `README.md`, and Makefile
targets for the deleted documentation site. These remain follow-up cleanup work;
their presence does not mean the deleted implementations survive.

The series also fixes sidebar rendering inside inert native `<template>` elements
(`77cefdd0`) and adds small replacement helpers for retained Docker-command and
approval behavior; this was not a deletion-only diff.

### Repository housekeeping — 2026-10-17

Deleted 626 GitHub branches and pruned remote references. Only `main` and
`stripped` remain locally and on GitHub. This does not affect the LOC totals.

## Frontend test removal — 2026-10-17

Removed the frontend test suite that remained at the initial baseline:

- Deleted `frontend/tests/`, including unit tests, fixtures, setup, and helpers.
- Deleted `frontend/src/components/__tests__/` and
  `frontend/src/services/__tests__/`.
- Deleted `frontend/vitest.config.ts`.
- Removed the `test`, `test:ui`, and `coverage` npm scripts and frontend test
  invocations from the Makefile; removed the obsolete TypeScript test exclusion.
- Removed Vitest, its coverage/UI packages, Vue Test Utils, and jsdom from direct
  development dependencies, and pruned their lockfile entries.
- Removed test instructions and examples from `frontend/README.md`.

**315 files deleted**, containing **47,824 physical lines**: 311 test files plus
four setup/helper/fixture/config files. cloc counts 38,595 TypeScript source LOC,
47 JSON fixture lines, 3,753 comment lines, and 5,429 blank lines in those files.

Including dependency, command, and documentation cleanup, this step changes
320 tracked files: **49,108 lines deleted, 11 inserted, net 49,097 lines removed**.
These diff totals exclude this log, which was still untracked when measured.

Using the original source/build LOC definition, the additional reduction is
38,598 LOC (38,595 TypeScript plus three Makefile lines). The working tree now
contains **145,630 source/build LOC**, or **21.62%** of the original 673,635 LOC.
The cumulative net reduction from `main` is **528,005 LOC (78.38%)**.
The initial baseline section above remains a historical snapshot.

Validation: checked that frontend test files and test-runner references in active
package/config/build files were removed, verified package/lockfile consistency,
and ran `git diff --check`. No frontend build was run; dependencies are not
installed in this checkout. Historical verification reports remain under
`frontend/verification/`.

## Remaining feature inventory — 2026-10-17

Snapshot: `de6ad1107c1bde1c9a3b0ebd223e3b28cc4da6ad`, after frontend test
removal. No feature code was changed during this analysis.

Measured with cloc 2.06, using the same source/build language set and excluding
comments and blank lines. The full repository has 145,630 source/build LOC.
The application trees (`internal/`, `cmd/`, `frontend/`, `web/`) contain 143,913
LOC; the other 1,717 are outside those trees, including documentation-associated
source and root build files.

The table assigns each counted application file to exactly one primary feature.
These are **file-attributed sizes, not exact deletion savings**. Files with
multiple responsibilities remain in shared infrastructure; for example,
`internal/server/mcp.go` (4,698 LOC), `internal/httpapi/server.go` (3,847),
`internal/server/server.go` (2,453), and `internal/runtime/runtime.go` (2,256)
contain wiring and logic for many features. Removing a feature would also trim
those files. A file's inclusion does not prove it is reachable or all of its code
is required by that feature. Generated contracts are included.

### Features, largest first

| Feature | Attributed LOC | Files | Largest implementation files |
| --- | ---: | ---: | --- |
| Upstream connections & server management | 16,483 | 55 | `frontend/src/views/ServerDetail.vue`, `internal/upstream/manager.go` |
| Activity history, usage, sessions & audit | 12,435 | 43 | `cmd/mcpproxy/activity_cmd.go`, `frontend/src/views/Activity.vue` |
| Profiles, scoping & access policy | 11,808 | 78 | `cmd/mcpproxy/profile_cmd.go`, `internal/runtime/profiles_service.go` |
| Upstream OAuth | 11,598 | 37 | `internal/upstream/core/connection_oauth.go`, `internal/oauth/config.go` |
| Client identity, credentials & agent tokens | 9,964 | 47 | `internal/runtime/credentials_service.go`, `frontend/src/views/AgentTokens.vue` |
| Quarantine, approvals & change review | 7,031 | 42 | `internal/runtime/tool_quarantine.go`, `cmd/mcpproxy/review_cmd.go` |
| Tool discovery, search & routing modes | 6,777 | 21 | `frontend/src/views/Tools.vue`, `cmd/mcpproxy/tools_cmd.go` |
| Configuration, hot reload & settings | 6,026 | 25 | `internal/config/config.go`, `internal/config/loader.go` |
| Code execution & stored scripts | 3,798 | 19 | `internal/server/mcp_code_execution.go`, `internal/jsruntime/runtime.go` |
| Diagnostics & remediation | 3,581 | 23 | `cmd/mcpproxy/doctor_cmd.go`, `internal/diagnostics/registry.go` |
| Metrics, tracing & health status | 2,492 | 12 | `internal/health/calculator.go`, `cmd/mcpproxy/status_cmd.go` |
| Secrets & secure environment | 2,487 | 16 | `frontend/src/views/Secrets.vue`, `internal/secret/resolver.go` |
| Call preflight / dry-run | 1,799 | 8 | `internal/server/preflight_glue.go`, `internal/preflight/evaluator.go` |
| Attention dashboard | 1,549 | 10 | `internal/runtime/attention_subscriber.go`, `frontend/src/views/Home.vue` |
| Concurrency limits & load shedding | 1,436 | 11 | `internal/upstream/dialgate/gate.go`, `internal/upstream/limiter/registry.go` |
| Log files & rotation | 1,049 | 5 | `internal/logs/logger.go`, `internal/logs/attribution.go` |
| Tool-response caching | 835 | 2 | `internal/cache/manager.go`, `internal/cache/models.go` |
| Client header forwarding | 775 | 10 | `internal/headerfwd/sink.go`, `internal/server/forward_headers.go` |
| TOON result serialization | 432 | 9 | `internal/server/toon_encode.go`, `internal/toonenc/encoder.go` |
| Token counting & savings estimates | 415 | 5 | `internal/server/tokens/tokenizer.go`, `internal/server/tokens/savings.go` |
| Prompt aggregation & refresh | 345 | 4 | `internal/upstream/manager_prompts.go`, `internal/upstream/core/prompts.go` |
| Response truncation | 293 | 2 | `internal/truncate/truncator.go`, `internal/server/mcp_max_result_size.go` |
| Tool output-schema validation | 247 | 6 | `internal/outputvalidation/validator.go`, `internal/server/output_validation.go` |

### Shared infrastructure

These counts are separate from the feature rows above; all rows together total
143,913 LOC.

| Area | LOC | Files |
| --- | ---: | ---: |
| Shared runtime, MCP/REST glue & contracts | 22,258 | 44 |
| Shared web UI & assets | 5,848 | 39 |
| Shared persistence & config transactions | 5,843 | 13 |
| Shared CLI & daemon client | 3,975 | 22 |
| HTTP/stdio transport, sockets & local TLS | 2,334 | 24 |

### Useful removal boundaries

- **Profiles + client identity/credentials:** 21,772 attributed LOC combined.
  This is the largest optional policy layer if the goal becomes a single-user
  proxy without per-client/per-profile filtering or agent-token lifecycle.
- **Activity/usage/sessions/audit:** 12,435 LOC. This is separate from log files
  and rotation (1,049 LOC). Removing historical reporting need not remove basic
  operational logging.
- **Upstream OAuth:** 11,598 LOC. Removing it would give up OAuth-authenticated
  upstream servers; static headers/secrets and unauthenticated connections are
  separate capabilities.
- **Quarantine/review:** 7,031 LOC. Removing it changes how new servers and changed
  tools/prompts become callable; it is a behavior decision as well as a code cut.
- **Code execution/stored scripts:** 3,798 LOC. A distinct alternative tool-call
  path, with Goja/esbuild dependencies; normal tool calls are separate.
- **Diagnostics/remediation:** 3,581 LOC. Mostly doctor/fix workflows and diagnostic
  explanations; basic error handling is still shared/core code.
- **Metrics/tracing/health:** 2,492 LOC. Includes health/status display, so dropping
  exporters alone would remove less than this entire row. Prometheus and
  OpenTelemetry dependencies are associated with this area.
- **Preflight, attention dashboard, TOON, token estimates:** smaller optional
  surfaces at 1,799, 1,549, 432, and 415 LOC respectively.

The web UI is a cross-cutting removal option: `frontend/src/` has 25,340 code LOC
(excluding SVG), and `web/` has another 109. **25,449 LOC total**, already spread
across the feature/shared rows, not an additional amount to sum. This excludes
frontend build configuration and verification artifacts. REST APIs also serve
CLI clients, so removing the UI would not imply removing all HTTP API code.

Profiles/client policy and activity/audit are intertwined with dispatch, storage,
and configuration. Their combined file counts are an inventory, not a guarantee
of a clean deletion boundary. Prompt aggregation and caching likewise have
additional implementation in shared dispatch files. Before a removal, inspect
those call sites and decide which behavior should survive.

## Local exploration build — 2026-10-17

Built the remaining backend with `go build ./cmd/mcpproxy` and the frontend with
`npm run build` (including Vue/TypeScript checking). Both succeeded. Started the
Vite dev server at `http://localhost:3000/ui/`, proxying API requests to the backend
on `127.0.0.1:8085`. The backend uses an isolated empty configuration/data directory
at `/tmp/mcpproxy-debloat-preview-data`, with its binary at
`/tmp/mcpproxy-debloat-preview` and logs in the temporary data directory.
Verified authenticated API requests and the live event stream in the browser.
No production source changes were needed to launch it.

## Clients list: detected or manually added — 2026-10-17

Changed the clients roster to include clients detected through MCP connections
or explicitly added/configured by the user, including clients with an issued
credential that have not connected yet. Untouched preset catalog entries stay
hidden. An MCP initialize request counts as a detected connection. Removed the
obsolete config-presence stub, config-path column, and reload hints; added an
empty state explaining both ways clients can appear. No installed-app detection
or client-config parsing was added.

The initial traffic-only filter incorrectly hid manually added clients; corrected
it to retain explicit client credentials and configured connections alongside
observed traffic. Credential detail and management operations remain available.

Validation: backend and frontend builds passed. An isolated live check confirmed
that a fresh roster is empty, manually added custom and preset-named clients
appear before their first connection, and known/unknown clients appear after
MCP initialize requests. Restarted the local preview with the correction.

## Remove server quarantine — 2026-10-17

Removed server quarantine from configuration, connection policy, storage models,
HTTP/MCP management actions, CLI flags/commands, metrics, and frontend controls.
Deleted server approval/baseline storage and the separate inspection/exemption
lifecycle. Enabled servers connect normally; legacy `quarantined` config fields
are ignored. Removed redundant boolean literals and dead branches left by the
initial mechanical edit.

Tool approval remains: under manual trust, initial and newly discovered tools
are pending and changed tools require review. Approval checks bind the reviewed
fingerprint and server incarnation; stale approvals are rejected. Existing
automatic trust and explicit tool-quarantine opt-outs remain supported.

Validation: backend build, `go vet ./...`, frontend build/typecheck, and diff
whitespace checks passed. An isolated live server confirmed initial pending
tools, successful approval, changed-tool holds, stale approval rejection, new
tool holds, and removal of server-quarantine HTTP routes. The clients roster
smoke check also passed. Verification scripts stayed outside the repository.

Current source/build code LOC, using the same language set as the original
comparison: **142,411**, or **21.14%** of the original 673,635. Net reduction
from the original: **531,224 code lines**. This is a current whole-tree count,
not a count attributed solely to server quarantine.

### GitHub remote OAuth

GitHub's remote MCP endpoint does not support dynamic client registration.
URL-only OAuth setup therefore requires a registered OAuth/GitHub app client ID
and the credentials appropriate to that app, or a supported personal access
token in the Authorization header. The missing-client-ID error is separate
from quarantine. See [GitHub host integration documentation](https://github.com/github/github-mcp-server/blob/main/docs/host-integration.md).

### Public remote servers for unauthenticated exploration

Unauthenticated MCP initialize returned HTTP 200 for these endpoints during
this session:

- [DeepWiki](https://docs.devin.ai/work-with-devin/deepwiki-mcp): `https://mcp.deepwiki.com/mcp`
- [Microsoft Learn](https://learn.microsoft.com/en-us/training/support/mcp): `https://learn.microsoft.com/api/mcp`
- [Grep](https://vercel.com/blog/grep-a-million-github-repositories-via-mcp): `https://mcp.grep.app`

Also documented as usable without an API key, but returned HTTP 403 from this
environment and were not live-verified here:

- [Exa](https://exa.ai/mcp): `https://mcp.exa.ai/mcp`
- [GitMCP](https://gitmcp.io): e.g. `https://gitmcp.io/fastapi/fastapi`

## Leftover cleanup: benchmark export, telemetry notes, stale help and README — 2026-10-18

Deleted `internal/server/bench_export.go` (the tool-definition export for the
removed `bench/` harness; it had no remaining callers) and the `bench-discovery`
Makefile target and help entry, which ran the deleted harness. Removed the stale
first-run telemetry notice comments in `cmd/mcpproxy/main.go`.

`mcpproxy upstream add` help and its post-add hint pointed at removed
`security scan` / `security report` / `security approve` commands and the macOS
app, and suggested `review approve <name> --yes`, which now fails without
`--all` or `--tools`. Both now list `review fetch`, `review show` and
`review approve <name> --all --yes`, and mention only the Web UI.

README: removed Windows install/download instructions, the scanner plugin,
Docker isolation, sensitive-data detection, output sanitisation and
token-savings claims, the tray/menu-bar app mentions outside the Homebrew
install block, and the scan-based quick-start steps. The quick start and the
"Review before use" summary now describe tool approval under `manual` and
`auto` trust. The install section still points at upstream release channels.

Validation: `go build ./cmd/mcpproxy`, `go vet ./cmd/mcpproxy ./internal/server`
and `gofmt -l` passed; an isolated `upstream add` printed the new hint.

## Frontend: Servers landing page and dark theme only — 2026-11-07

Deleted the homepage and its standalone attention-list component. Root and
legacy overview URLs redirect to Servers, preserving query strings and hashes.
Servers is first in the sidebar's Connect section; Home is removed from sidebar
and command-palette navigation. The header attention popover now shows all items
in a scrollable list instead of linking to the removed homepage.

Deleted the project-links footer and its link configuration, and removed the
sidebar Docs link. Removed theme selection, persisted theme state, OS colour
scheme listeners, the theme type, unused colour-math helpers, and alternate-theme
CSS. The document selects Dark before JavaScript loads, and DaisyUI builds only
that theme. Existing contextual documentation links inside feature/settings
controls are unchanged.

Frontend source diff: 19 files affected, 1,368 physical lines deleted,
31 added; **1,337 net physical lines removed** (excluding this log).
Validation: Vue/TypeScript check and Vite production build passed; diff whitespace
check passed. Local Vite demo serves these changes without a backend restart.

### Fix startup after homepage removal — 2026-11-07

The app still relied on a transition of `authStore.canLoadCore` to initialize
shared data and the SSE connection. That value is now always true after removal
of session auth, so the watcher never fired. The homepage had masked this by
loading server data itself. Replaced that watcher with direct initialization on
mount and after validated API-key recovery; removed obsolete login redirects
from these paths. Verified in the browser after a full reload: SSE connected,
server data loaded, and the empty state rendered. Frontend build/typecheck and
whitespace checks passed.

## Remove frontend Hints panels — 2026-11-07

Removed all collapsible Hints blocks from Servers, Server details, Tools, Secrets,
and Settings, including their hint text and command examples. Deleted both
`CollapsibleHintsPanel.vue` and the unused older `HintsPanel.vue`, their types and
styles, and the settings helper used only to generate hint content.

Removed **865 physical lines across 8 files**, excluding this log. Verified that
no panel references remain in frontend source. Vue/TypeScript checking, Vite
production build, and diff whitespace checks passed. The local Vite demo picks
up the changes directly.

## Populate local demo from main instance — 2026-11-07

Copied 12 configured upstream servers (10 enabled) from the main instance into
the isolated temporary demo config, retaining the demo API key and settings.
Copied 3 saved OAuth records from a validated filesystem snapshot of the main
database; the main daemon stayed running. The 12 keyring references resolve
through the existing macOS keychain, without copying their values into config.
The temporary source database snapshot was deleted after import.

Verified all 10 enabled servers connected; the remaining 2 stayed disabled. All
12 referenced secrets report as set. Keychain reads work; writes remain disabled
by the existing non-tray opt-in policy. Backed up the previous demo data before
import. No credentials or personal server configuration were added to Git.

## Simplify the review queue — 2026-11-07

The queue now lists every server with new or changed tools awaiting review,
including disabled servers. Removed the Active blockers/All reviews selector,
search, sort controls, disabled-server badges, and blocker summary text. Removed
the sidebar blocker tooltip and its active/deferred calculations. Queue rows use
a fixed name order; individual tool reviews and approval controls remain.

Removed the frontend triage helper and queue query filters. Direct approval-fix
links now open the server's review rather than filtering the queue. Backend rows
now contain only server name and pending/changed counts: removed enabled-state,
age calculation, and unused server-review/tier metadata. Updated CLI queue output
to the same three fields. There were no backend search, sort, or active-only
request options to remove; caller access checks remain in place.

Validation: frontend build/typecheck, backend build, vet, and whitespace checks
passed. An isolated live check using a temporary demo snapshot confirmed that
all 10 reviews remain listed with every server disabled, with only the expected
three row fields in fixed name order. Temporary check data was deleted.

## Review queue: inline server sections — 2026-11-07

The review queue is now a single page with one section per server, containing
all tools that need review. Each section has Approve all, and individual tools
retain Approve/Block, descriptions, schemas, previous definitions, and diffs.
Removed the dedicated review screen/list components, frontend per-server review
fetch/type, tool search/state filters, pagination, and server configuration panel.
The Server details Review tab now links to the queue rather than embedding a
second review screen. Tool/server/attention links target section anchors; old
per-server page URLs redirect there.

The queue endpoint now returns pending/changed tool definitions, fingerprints,
and the server incarnation directly. It reuses the shared review builder. The
per-server backend read API remains because CLI show/fetch/approval workflows
also use it; it is no longer called by the frontend. Approval writes still bind
to the displayed definitions and incarnation.

Validation: frontend build/typecheck, backend build, vet, and whitespace checks
passed. An isolated live check verified all 10 disabled servers still appear,
with only pending/changed tools, fingerprints and incarnations. Stale approval
was rejected; approving every displayed tool removed only that server section.
Temporary check data was deleted; main-instance credentials and approvals were
not changed by the check.

## Remove repeated tool-description caption — 2026-11-07

Removed the hardcoded “from the server, not verified” caption from the shared
tool-description component. Tool descriptions still render as plain text.

## Label pending tools as New — 2026-11-07

Changed the frontend display label for pending tool approvals to New, including
review badges, the Tools filter, server summaries, and tool-review help text.
Kept the internal/API status `pending` to avoid a storage or protocol migration.
Frontend build/typecheck and whitespace checks passed.

Adjusted the pending-tool display label to lowercase `new`.

## Simplify Servers page header and filters — 2026-11-07

Removed the entire large summary-card row (Total Servers, Connected, Total Tools)
and its unused count/summary computations. Removed the “Manage upstream MCP
servers” subtitle. Moved the profile selector into the heading row alongside
Servers and Refresh, replacing its visible Profile label with an accessible
name. Profile filtering and removable filter chips remain available.

Added Disabled beside All, Connected, and Enabled, with a count and filtering of
servers whose enabled setting is off. The new status participates in existing
URL and profile filtering. Frontend build/typecheck and whitespace checks passed;
the local Vite demo serves the changes directly.

## Remove remaining server-quarantine references — 2026-11-07

Removed the Server details “Quarantined: No” row: it was hardcoded to false and
had no backing server state. Removed unreachable server-quarantine empty-state
text and obsolete trust-mode admission notes. Trust modes now describe tool
approval only; corrected the remaining unsupported Scan-mode hint.

Deleted unused server-quarantine preflight/status/error enums and branches,
permission operations, activity event/handler/emitter, inspection-only helpers,
scoped-stat bookkeeping, test hook, and constant-false diagnostic output. Updated
MCP descriptions, CLI diagnostics, connection errors and stale comments so they
no longer claim servers can be quarantined. Tool approval/quarantine is retained.
Historical log entries and this removal history are not rewritten.

Validation: frontend build/typecheck, backend build, vet and whitespace checks
passed. Source scans found no remaining server-quarantine labels, operations, or
error codes in the active Go/frontend implementation. An isolated live check
confirmed disabled servers remain reviewable, stale approvals are rejected, and
approving a server's displayed tools removes only that review section. Temporary
check data was deleted. Restarted the local demo with the cleanup.

## Simplify Server details header — 2026-11-07

Removed the four large Tools, Admin state, Protocol, and Health summary cards,
the Servers → server breadcrumbs, and the protocol/endpoint line beneath the
heading. Removed computations used only by those cards. The server heading,
status badge, action menu, configuration and tool controls remain.

Frontend build/typecheck and whitespace checks passed. Changes are served by
the local Vite demo without a backend restart.

## Server configuration: Trust all tools toggle — 2026-11-07

Replaced the standalone Trust mode card with a small Trust all tools toggle in
General, beside the existing Enabled setting. Off maps to manual review and is
the default when no mode is configured; On maps to auto approval of new and
changed tools. Existing explicit settings are preserved. Saves use the existing
trust_mode API, disable the toggle while saving, and retain the stored setting
on failure. Updated the tool-review help link to name the toggle. Removed the
old card, selector import, and restart-notice state from this page.

Also removed the Configuration tab's Status and Health cards and helpers used
only by them, including timestamp formatting and duplicate health presentation.
The header's existing status indicator remains. Frontend build/typecheck and
whitespace checks passed; the local demo picks up the changes directly.

## Remove inline reviews from Server details — 2026-11-07

Removed the Review tab/link and the orange tool-review banner/cards above the
regular Tools list. Deleted their approval/block handlers, transient state,
hold-evidence component, diff helper and inline diff algorithm. Removed the
extra per-changed-tool diff requests and the now-unused frontend API method.

Server details retains its regular tool inventory, enabled/disabled controls,
and approval status badges. Approval metadata still supports those controls;
review decisions live on the separate Review queue page. Legacy ?tab=review
links continue to redirect there. Frontend build/typecheck, reference scans,
and whitespace checks passed; the local Vite demo serves the changes directly.

## Replace DaisyUI with shadcn-vue — 2026-11-07

Replaced the frontend component and styling layer with owned shadcn-vue
components, retaining Vue, the existing routes/navigation/layout, stores and API
behavior. Matched Steam Lancache's dark design tokens exactly: black canvas,
charcoal cards/popovers, gray borders and muted text, white primary actions,
and restrained status colors. Updated chart colors to the same palette.

Removed the DaisyUI dependency, theme CSS, component classes and form-layout
compatibility rules. Migrated page controls to local Button, Badge, Card, Alert,
Input, NativeSelect, Textarea, Switch, Checkbox, Table, Skeleton and Spinner
components. Menus/popovers use shadcn-vue/Reka UI; the existing sidebar geometry,
mobile navigation, and Activity details sheet remain. Native dialogs preserve
browser focus trapping and top-layer behavior. Native form controls preserve
DOM change events, numeric input and checkbox-array semantics. Removed unused
generated components. Updated frontend documentation and Vite configuration.

Validation: production frontend build/typecheck and an embedded Go binary build
passed. An isolated DOM harness loaded all navigation pages, verified the 12
server cards and Disabled filter, action menus, stored trust setting, all 10
review sections, numeric inputs, selects, switches, checkbox arrays, button
events and dialog lifecycle. Compared the palette to Steam Lancache's source
and checked for unresolved components/runtime errors. This was a DOM interaction
check, not a browser visual audit; the external Monaco AMD loader was stubbed.
Verification made no API writes and introduced no repository test suite. Diff
whitespace checks passed. Restarted Vite and rebuilt the demo backend with the
new embedded frontend; server configuration and credentials were retained.

## Redesign the frontend around shadcn patterns — 2026-11-07

Reworked the page designs after the component migration, using Steam Lancache's
Statistics line tabs and dark palette as the reference. Kept the same navigation,
routes and page responsibilities.

- Added real shadcn/Reka Tabs, with keyboard navigation, focus management and
  associated panels. Applied them to Servers, Tools, Secrets, Clients, Activity,
  Settings, server details and the routing-settings popover. Counts are quiet
  text beside each tab label.
- Made page headings, secondary actions, spacing, card borders, table headers,
  table rows and dialogs consistent. Removed duplicated Button appearance
  overrides and empty class attributes. Reduced competing top-bar surfaces.
- Made server cards compact summaries, with linked names, transport text,
  status dots, metadata and a separated action footer; removed their fixed
  minimum height.
- Replaced the oversized Tools statistics with status tabs, removed the duplicate
  status selector, and retained config-denied filtering as a tab. Changed token
  status and Usage time windows to compact selects. Reduced Activity's expanded
  status counters to quiet controls.
- Rendered Secrets as divided rows rather than individual cards; moved counts
  into tabs and kept missing/migration information as a small summary.
- Grouped Review tools into one bordered section per server, with divided rows,
  quieter per-tool actions and expandable schemas/changes. All tools and each
  server's Approve all action remain present.
- Cleaned up Settings forms, removed the repeated security-summary badges,
  replaced secret-field emoji buttons with labeled Lucide icons, and kept drafts
  mounted when switching tabs.
- Removed Add Server's single-option Manual tab and its unused navigation logic.
  Replaced its large trust-mode radio cards with the same Trust all tools switch
  used by server configuration, off by default. Deleted TrustModeSelector and
  its unused warning-selection metadata.
- Fixed a migration bug in Checkbox: Vue's implicit false default for an omitted
  boolean modelValue overrode an explicitly supplied checked value. Both binding
  forms now show the saved state correctly, including enabled-server switches.
- Documented these conventions in frontend/README.md.

Validation: frontend production build/typecheck and the embedded Go binary build
passed. The isolated DOM harness verified all main routes, all 12 server cards,
Disabled filtering, menus, saved on/off switch states, Add Server's default and
trust toggle, routing-popover tabs, all 10 review sections, native controls and
dialog lifecycle. It
reported no unresolved components or runtime errors. Browser inspection covered
the main pages at desktop width and all main routes, server configuration and
Add Server at 390px, with no page-level horizontal overflow. Checked keyboard tab
activation and Settings draft preservation across tabs. Verification made no API
writes; the DOM harness stubs the external Monaco loader. Diff whitespace checks
passed. Rebuilt the local demo's embedded frontend, retaining its configuration
and credentials.

## Simplify server-card status and transport — 2026-11-07

Removed the server card's trust shield and separate status line, together with
unused badge/detail computations. Moved the connection indicator immediately
before the server name: green for connected, yellow for connected with new or
changed tools awaiting approval, red for connection/authentication/configuration
problems, and white for disabled. Disabled takes precedence over stale health;
errors take precedence over pending reviews. Kept an accessible status label
and hover detail. Replaced transport text with Lucide Globe (remote) or Terminal
(stdio), with accessible labels and hover text.

Validation: frontend production build/typecheck, embedded Go build, DOM
interaction checks and diff whitespace checks passed. Inspected all 12 demo
cards: nine yellow, two white, and one red; adrafinil-keepawake is yellow with a
terminal icon. No cards retain the shield or status row. Rebuilt the demo's
embedded assets; server settings, approvals and credentials were not changed.

## Outline the CLI transport icon — 2026-11-07

Replaced the bare Terminal glyph on server cards with Lucide SquareTerminal,
which encloses the prompt in an outlined terminal window. Kept its size and
accessible transport label. Frontend production build/typecheck passed.

## Reduce server-card metadata and align filters — 2026-11-07

Removed the divider above card actions and the error/24-hour text. Cards now
show `N tools (X need approval)` (approval text only when nonzero), followed by
`last call …` or `never used` on its own line. Removed the old verbose approval
summary and error-count computation. Activity links now open the server's full
history instead of a 24-hour/error-filtered view.

Added `period=all` to the existing activity-summary endpoint and used it for the
server grid, so an older call does not become “never used” just because it fell
outside 24 hours. Existing period defaults and authorization filters remain.
“Never used” reflects the available retained activity history.

Moved the profile selector from the page heading into the filters row, directly
before search, alongside the status tabs. Kept it available when a selected
profile has no servers, and let the row wrap on smaller screens.

Validation: frontend production build/typecheck, Go HTTP API package compilation,
and embedded binary build passed. Refreshed the local demo with the new assets
and activity-summary behavior. No server configuration or approval changes.

## Make Settings a regular sidebar item — 2026-11-07

Moved Settings to the last position in Monitor, using the same menu styling and
active state as Activity and Usage. Removed the separate footer navigation and
its SIDEBAR_FOOTER model; the command palette now finds Settings through the
shared groups. Removed internal sidebar dividers, including the version and
logo separators, and retained the version text at the bottom.

Validation: frontend production build/typecheck, embedded Go binary build and
diff whitespace checks passed. Updated the local demo.

## Compact server cards and move last usage — 2026-11-07

Moved last usage into the name row immediately before the transport icon. Used
short relative times (or “never used”), with the full timestamp in its hover
label. Reduced card padding, gaps, title size and action-button height. Put tool
counts and actions together on the second row, removing the separate usage and
action rows. Preserved status dots, transport labels, approval counts and actions.

Validation: frontend production build/typecheck, embedded Go build and diff
whitespace checks passed. Updated the local demo.

## Remove server tabs and group cards by status — 2026-11-07

Removed Servers' status tabs, tab panels, status-filter state/counts and URL
writeback. Old status parameters are cleared instead of silently narrowing the
list. Removed Servers from the shared status-filter scope registration and
updated Tools' server link to stop generating a disabled-only URL. These tabs
were entirely client-side; no backend endpoint or filtering implementation
existed exclusively for them.

Shared one status classification between card dots and sorting: red first,
then approval/orange, connected/green and disabled/white. Within each group,
sort alphabetically by displayed server name. Removed the card's brittle manual
v-memo dependency list so live status changes cannot leave stale dots behind.

Narrowed the responsive grid to three columns at the demo's desktop width and
four on wider screens. Moved profile and search beside Refresh in the heading;
controls wrap on small screens without squeezing search to an unusable width.

Validation: frontend production build/typecheck, embedded Go build, DOM
interaction checks and whitespace checks passed. Verified all 12 demo cards'
status/name order, absence of tabs, removal of old status URLs, retained menus
and filters, and shared control-row placement. Browser inspection confirmed
three ~317px columns at 1280px and no page overflow at 390px. Updated the demo;
no server configuration, credentials or approvals were changed.

## Link card review counts directly to the review queue — 2026-11-07

Removed total tool counts from server cards. Cards now show only an orange
`X tools to review` link when new or changed tools need review; the text is
omitted when the count is zero. Each link targets the existing review-queue
anchor for that server. Last usage remains in the name row.

Validation: frontend production build/typecheck, embedded Go build, DOM checks
and whitespace checks passed. Browser navigation from Slack's card opened
`/review#review-slack` and scrolled its section to the top of the content area.
Verified the compact grid, status/name ordering and inline controls from the
preceding change remain intact. Updated the demo without changing approvals.

## Remove card menus and brighten status dots — 2026-11-07

Removed the three-dot menu and card-only delete modal, logout handling, toggle
handling, trust label and other unused menu computations/imports. Enable,
restart/sign-in remedies and Details remain as contextual card actions.

The detail page already provided enable/disable, restart/reconnect, sign-in,
logs and configuration. Moved the previously card-only Delete and OAuth logout
into its Actions menu. Delete uses a named confirmation dialog and returns to
Servers on success. No server was deleted or logged out during verification.

Changed only the red/yellow/green card dots to vivid Display P3 colors, with
bright sRGB fallbacks. Disabled remains plain white. Removed the initially
added glow per clarification; dots have no shadow or halo.

Validation: frontend production build/typecheck, embedded Go build, DOM
interaction checks and whitespace checks passed. Verified cards have no menu,
status ordering remains correct, and detail Delete opens a named confirmation
and cancels without an API write. Updated the demo.

## Make server cards clickable — 2026-11-07

Removed the Details button and stretched the server's native router link across
the card, retaining keyboard focus and normal link behavior. Review links and
contextual action buttons sit above that link so they keep their own actions
without nested anchors or a competing card click handler. Last usage is now
informational within the clickable card. Empty footer rows are omitted.

Changed the orange link to `Review N tools` (singular for one tool). Validation:
frontend production build/typecheck, embedded Go build, DOM checks and whitespace
checks passed. In the browser, clicking blank card padding opened the correct
server, while clicking Review opened the review queue and scrolled to its server
section. Updated the local demo.

## Move last usage and remove card action buttons — 2026-11-07

Moved last usage to the left of the second line and aligned `Review N tools`
to its bottom-right corner. Removed all contextual card action buttons,
including Sign in and Enable, and their action dispatch, API handlers, loading
state and unused imports. Cards now only display status/name/transport plus
last usage and the optional review link. Clicking the card opens server details;
review remains a separate link. Server actions remain on the detail page.

Validation: frontend production build/typecheck, embedded Go build, DOM
interaction checks and whitespace checks passed. Verified usage and review share
the second line and no card action buttons or Details button remain. Updated the
demo; no server settings, authentication or approvals were changed.

## Show short errors on red server cards — 2026-11-07

Red cards now replace the review link with a small error message at the bottom
right. Labels distinguish Needs sign-in, Failed to start, Failed to connect,
Missing secret, Needs configuration and general server errors/attention.
Transient connections say Starting/Connecting rather than reporting a failure.
Non-error cards retain Review N tools when needed. Error labels use the same
status classification as the dots and sort groups, so fixing a connection
restores the review link automatically when reviews remain.

Validation: frontend production build/typecheck, embedded Go build, DOM checks
and whitespace checks passed. Verified red demo cards show a short error and
no review link, while connected servers keep their review links. Updated the
local demo without changing server settings or approvals.

## Unify all tool browsing and review — 2026-11-07

Tools is now the single tool inventory, grouped into server sections. Removed
the standalone Review view and ReviewSection component, the Review queue sidebar
entry, and the server detail Tools tab/schema modal plus its independent tool,
approval, toggle, bulk-action and live-refresh machinery. Server details default
to Configuration and link to Tools filtered to that server. Old review URLs,
review anchors, server-tools tabs and tool-location links redirect to Tools.
The Tools navigation badge now counts tools requiring review.

The one row design supports compact browsing and expandable definitions.
Search, server and Needs review are the primary controls; availability, tier,
approval and view-as filters remain under More filters. Review links open the
same list filtered to a server's pending/changed tools with details expanded.
Each server section can approve all visible reviews; individual rows support
Approve/Block, and reviewed tools retain availability switches. View-as stays
read-only with its scope, access verdicts, explanations and visible/hidden counts.

Kept full descriptions, input AND output schemas, all annotation values,
usage/last-used data, intrinsic/profile tiers, access information, hold evidence,
previous definitions, change diffs, fingerprints and complete approval records.
The raw metadata remains inspectable as well. Review actions bind to the exact
captured definition and server incarnation; metadata loads cannot replace that
review snapshot. Filtered bulk approvals explicitly say Approve shown.

Fixed the underlying catalogue data gaps: the typed REST tool DTO and its
producers/converter now carry output schemas, including indexed and stored
fallbacks. Saved approval definitions remain in the catalogue when a server is
offline, including after approval; cached definitions are labeled. The existing
diff endpoint can optionally return unchanged/pending approval records for full
history/metadata, while its default changed-only behavior remains intact.
Shared REST review endpoints remain because the unified inventory and CLI use
them; they no longer support a separate frontend list.

Validation: frontend production build/typecheck and Go contracts/API/server/
runtime package checks passed. Added a regression test proving output schemas
survive conversion and JSON serialization as structured schemas, alongside input
schemas. On an isolated cloned database, verified disabled-server inventory,
output schemas before/after approval, stale-fingerprint 409 rejection, and full
approved records. No real demo approval/settings writes were made. The DOM
harness checked complete inventory merging, schemas/annotations/history,
legacy redirects, removal of the detail Tools tab, scoped read-only controls,
and simulated approval/block/toggle payloads including incarnation/fingerprint
bindings. Browser checks covered desktop/390px layouts and review schema
visibility without page overflow. Rebuilt and refreshed the local demo.

Tool review badges use “New” and “Changed”. Legacy tool/review redirects preserve authentication and view-scope query parameters.


## Remove configuration tool allowlists and denylists — 2026-11-07

Removed `enabled_tools` / `disabled_tools` from server configuration, persisted
upstream records, copies, validation, and all callability/discovery/preflight
checks. Old files and database records can still be read; these retired keys
are ignored, rather than becoming another layer of tool availability policy.
The per-tool stored `Disabled` flag, controlled by the UI/API/CLI enable/disable
action, is now the tool availability control. Tool approval, server enabled
state, and client/profile access rules remain in place.

Removed the `config_denied` API field, enable-request config conflicts,
configuration-denied status/count/reason enums, CLI filter/display state, and
frontend lock label, availability filter, and disabled-switch guards. Updated
current API/CLI/preflight docs and the published extension schema. Historical
design specs are retained as history.

Validation: frontend typecheck/production build, `go test ./...`, and the unified
Tools DOM checks passed. An isolated local stdio MCP fixture with BOTH retired
config keys loaded successfully and remained discoverable/callable. The real
UI availability endpoint disabled calls, then restored them without a config
conflict; disabled state persisted across a daemon restart. All test mutations
used a temporary standalone database and fixture, leaving user/demo tool
settings unchanged. Rebuilt and refreshed the local demo.

Approval filter options now also display “New” and “Changed”, matching the tool badges.


## Simplify the Tools filter bar — 2026-11-07

Removed the approval filter and its filtering logic. Availability, tier, and
view-as now sit directly alongside search, the server selector, and Needs
review in one wrapping toolbar. Removed the More filters disclosure/popover.
Tier labels use the shared display labels. Legacy approval query values no
longer filter the inventory.

Validation: frontend build/typecheck and DOM checks passed, including inline filter placement, ignored legacy approval queries, and preserved review actions. Refreshed the local demo.


## Keep Tools interactions local and stable — 2026-11-07

Fixed the scope watcher: it compared a freshly created array on every URL
change, so even local search/server/tier/availability/Needs review filters
cleared and fetched the inventory. It now watches the individual subject values;
only actual client/profile changes reload scope and clear the old subject data.
Local filters no longer reset scroll or expand every pending tool. Explicit
server/tool deep links still scroll/focus their target.

Tool actions patch confirmed results directly into the affected rows. Pending
state belongs to each tool, so other tools remain usable, including concurrent
edits within a server. Availability switches reflect the pending value and roll
back on failure. Ordering stays alphabetical instead of moving a newly approved
tool to another part of the list. Review requests retain captured fingerprint
and incarnation checks; partial responses trigger reconciliation rather than
claiming every selected tool changed.

Background/SSE reconciliation no longer spins the page refresh icon, disables
all controls, or clears details. Repeated events coalesce, requests made before
a write cannot overwrite its result, and reconciliation waits for in-flight
writes. Completion remains tracked even when filtering unmounts its section.
Detail records are invalidated only when that tool's definition/state changes,
with response version guards, preserving other expanded rows and open schemas.
Removed the redundant global server fetch after every tool action.

Validation: production build/typecheck and both isolated DOM checks passed.
Verified zero inventory requests for local filters, stable row/detail nodes and
scroll, row-scoped pending controls, immediate confirmed approvals, concurrent
edits, rollback on errors, stale response protection, coalesced live updates,
completion after filtering out a section, and safe view-as loading. Existing
schema/history/annotation and review-binding checks also pass. All write requests
were simulated; real tool approvals and settings were untouched. Rebuilt and
refreshed the local demo.

Restored automatic expansion when Needs review is selected, as requested. Explicitly collapsed tools stay collapsed. Local filtering, stable details, and quiet row updates remain in place.


## Simplify and format expanded tool details — 2026-11-07

Removed the Approval field, Approval record section, Load full approval record
button and All tool metadata section. Removed the record-fetch API helper,
per-tool record cache/loading/version machinery, and the backend's UI-only
`include_unchanged` diff option. The ordinary changed-tool diff endpoint remains.
Schemas, annotations, previous definitions and diffs come from the existing
inventory/review snapshots, retaining their review bindings.

Descriptions now render sanitized Markdown in a padded pure-black (#000)
block, with typography for headings, lists, links, code and tables. The existing
Show all/Show less behavior remains. Added direct marked/DOMPurify dependencies
(they were already transitive dependencies) for parsing and sanitization.
Input/output schemas, annotations, change/previous-definition and hold-evidence
blocks share JSON syntax highlighting. JSON token text renders through Vue
interpolation, preserving exact data without interpreting it as HTML.

Usage, Last used and View calls now form one three-column row above the
description. Profile access details remain separate.

Validation: frontend build/typecheck and Go API/server/contracts package checks
passed. A jsdom-based DOM run verified Markdown and sanitization of script,
event-handler, style and javascript-URL payloads; rich Markdown, black-block
styling, JSON highlighting/round-trip data, removed diagnostic views, and stable
local filtering/actions. No real tool settings or approvals were changed.
Rebuilt and refreshed the demo.

JSON highlighting mounts only when its disclosure is open, avoiding large hidden schema DOMs. Removed the sidebar version text, its formatting logic and the version-bearing logo tooltip.

Tool usage/calls now use content-sized items with compact spacing instead of stretching across the row. View calls is an outlined link button. Empty annotation objects no longer create a disclosure; explicit false hints remain visible.

Recovered the original server-card Details implementation from the edit history and reused its exact ghost/xs button variants and ↗ arrow for View calls, replacing the guessed outline styling.

Replaced the Tools initial/scope-loading spinner with shadcn skeleton server sections and tool rows. Placeholders appear immediately during feature initialization; existing rows stay visible during background/manual refreshes. Loading status is announced accessibly, and reduced-motion styling remains respected.


## Remove startup connection/status flashes and sidebar counts — 2026-11-07

The connection warning no longer treats the initial disconnected boolean as
an outage. It appears only after a real EventSource error persists for two
seconds; successful opens, intentional disconnects and auth recovery cancel
pending warnings. Warning state survives retry attempts until recovery, so an
actual outage does not flash on/off. Successful opens now cancel the reconnect
timer too, preventing an old retry from interrupting a recovered stream.

The header uses a quiet skeleton until the first server list and routing data
have settled. It no longer paints loading text as a temporary status pill or
uses the missing initial SSE running value as evidence of a stopped server.
Real errors and live status changes still render.

Removed Servers and Secrets sidebar badges, their badge model entries and count
logic. Removed the secret-count request and the already-unused global-tool-count
request; Tools keeps its review badge and Clients keeps its live-client badge.

Validation: frontend build/typecheck and isolated jsdom checks passed for slow
startup, staged header responses, no initial error dot/toast, brief interruptions,
sustained loss, retry cancellation after recovery, auth suppression and retired
source/intentional disconnect cleanup. Confirmed badge and unused request removal.
All tests were read-only against the demo, with controlled local event streams.
Rebuilt and refreshed the demo.

Sidebar navigation rows now have a fixed 36px height and count badges a fixed 20px height, preventing Clients/Tools badge updates from shifting later rows. Tools still shows only a positive count of new/changed tools awaiting review.


## Remove the top bar and fix first-paint navigation — 2026-11-07

The app now waits for Vue Router's initial resolution before mounting, avoiding
the sidebar's temporary unselected state on a direct page refresh or redirect.
Removed TopHeader, StatusPill, AddMenu, ViewingFilter, the header-only attention
store, add-menu model entries, unused header z-index and info/status display
state. App no longer starts header info/routing/attention requests. Clients'
mode switcher loads routing when it mounts; other routing consumers remain.

CommandPalette mounts directly in the app shell. Ctrl/Cmd+K and `/` still open
it, keyboard selection/search/navigation remain, and typing in a field does not
trigger `/`. Removed the visible search launcher along with the rest of the
bar. A mobile-only drawer opener preserves access to the sidebar without a
replacement top bar.

Removed the backend `attention.changed` event payload/publishing, id-set tracking,
per-subscriber narrowing and suppression machinery; the deleted header was its
only remaining consumer. The cached attention snapshot and GET /attention remain
for CLI attention/status/doctor. GET /info, /status and /routing also remain
because CLI, client connection instructions, settings and routing controls use
them. Updated current REST/attention documentation.

Validation: frontend build/typecheck and `go test ./...` passed. A jsdom run
observed the first sidebar paint, verified its correct highlight, checked that
the bar and its private startup requests are gone, exercised Ctrl/Cmd+K, slash,
Escape, popup search navigation and editable-field handling, and opened/closed
the mobile drawer. Test streams/writes were controlled; user data was untouched.
Rebuilt and refreshed the local demo; shared CLI/API endpoints still respond.

Corrected the first-paint implementation: waiting to mount the entire app caused a black startup gap. The shell now mounts immediately, the sidebar resolves the initial URL/redirect for its highlight, and only the page-content area shows a skeleton while the first route component loads.


## Render stable page chrome on the first frame — 2026-11-07

Removed the generic route skeleton that replaced page headings and toolbars.
Route views now ship with the initial UI bundle, avoiding a second module-fetch
round trip before static page UI can render. Usage charts and the Settings JSON
editor remain lazy-loaded within their data areas. Removed the unnecessary async
wrapper on the title-only route guard. Vue remains in place; the flicker came
from routing/loading choices and capability-gated controls.

Servers renders its profile selector immediately instead of inserting it after
/status; profile-list loading no longer dims/replaces the control. Search starts
with the URL query. Refresh keeps one label, size and icon throughout initial
loading; only an explicit manual refresh gets a local busy state. Manual refresh
updates the existing cards silently. Initial server data uses skeleton cards,
never skeleton headings or controls. Tools' view-as selector is likewise stable
while capabilities load.

Validation: frontend production build/typecheck passed. With server/status/profile
responses deliberately held, a jsdom animation-frame check found the real Servers
heading and all controls on the first frame. Identical DOM nodes and labels
survived capability/profile/data responses; manual refresh retained real cards.
Sidebar-first-paint, hotkey popup/search navigation and mobile-drawer checks still
pass. No user settings or approvals changed. Rebuilt and refreshed the demo.


## Delay skeletons and align card placeholders — 2026-11-07

Added a shared 150 ms skeleton visibility delay for Servers, Tools, Profiles,
Profile editor and the access explainer. Fast requests never display skeletons;
slow requests reveal them after the threshold. Their actual loading branches
remain active during the delay, preventing false empty states. Completion is
never held back for a minimum display time. Pending timers are canceled on
completion and scope disposal; static headings/controls remain untouched.

Server placeholders now use the same Card/CardContent components as real cards,
with matching 12px padding, 8px row gap, 20px name row and 16px secondary row.
Skeleton bars are vertically centered within those rows, and transport/review
positions match the real content. Both card variants have the same 70px geometry
including borders. Fixed real row heights also remove tiny height variations
between cards with different secondary content.

The Tools review badge fades in/out over 160 ms using opacity only. Positive
count updates do not remount the badge; Clients retains its existing behavior.
Fixed sidebar row/badge heights remain, and global reduced-motion preferences
still disable animation.

Validation: frontend build/typecheck passed. Controlled DOM tests covered fast
loads without visible placeholders, slow Servers/Tools requests revealing them
after the delay, immediate completion without an artificial hold, shared card
padding/gap/row classes, and review-badge enter classes with stable sidebar row
height. No user settings or approvals changed. Rebuilt and refreshed the demo.

Reverted the 150 ms skeleton wait as requested. Removed the shared delay composable and visibility gates. Skeletons appear immediately again; matched card geometry and the review-badge fade remain.

Restored the 150 ms skeleton delay on request. Card alignment and review-badge fade remain unchanged.

Removed the descriptive subtext below Clients, Profiles, Secrets, Activity and Settings headings, along with their now-redundant heading wrappers. Header actions align with the title. Usage already had no heading description.


## Share the server Tools tab and simplify loading — 2026-11-07

Server detail now orders tabs Configuration, Tools, Logs. The server heading and
tabs render immediately; initial metadata and logs use delayed skeletons instead
of page spinners. Opening Configuration no longer fetches or waits for logs.
Removed the recent-log subtitle and the long empty-log explanation.

Extracted ToolInventory from the Tools page and reused it directly in the server
Tools tab, scoped to that server. Search, filters, view-as, schema highlighting,
descriptions, usage, approvals and enable/disable controls share one implementation.
Embedded tools omit the redundant server heading, count, outer card border and
header divider; bulk approval remains available above the tool rows. Skeletons
use the same ungrouped layout. Direct tool bookmarks stay within server detail.

Existing catalogue and review APIs accept an optional server query filter before
collecting their results, retaining caller visibility checks. Removed obsolete
server-quarantine checks from catalogue assembly. No new endpoint was introduced.

Validation: Go suite and frontend production build/typecheck passed. Controlled
DOM checks cover tab order, initial skeletons, lazy logs, shared filters/schemas,
server switching, direct bookmarks, read-only views and simulated inline writes
with snapshot bindings. No live approvals or settings were modified.

Rebuilt and refreshed the local demo. Read-only live checks confirmed the current
frontend asset and server-scoped catalogue/review results, including empty results
for unknown servers. Embedded/standalone DOM checks also confirmed the heading
and divider are omitted only inside server detail, with bulk approval retained.

Restored the card border and background around embedded server tools on request.
Only the redundant server heading and header divider are omitted; skeleton cards
match this container.

Moved embedded-server bulk approval into the shared filter toolbar immediately
before Refresh. The same button and snapshot-bound action are reused; standalone
server groups retain their own bulk controls.

Toolbar placement passed controlled DOM checks, including retained standalone
controls and read-only views. Frontend build/typecheck passed.

Needs review is visible only when the selected server scope contains tools needing
review. Once the loaded inventory has none left, the review filter clears itself
and disappears, restoring the normal list without fetching it again. The shared
behavior applies to standalone Tools and the server Tools tab.

Reserved fixed-height title and action rows in server detail before metadata
arrives. Loading status/actions no longer grows the header or moves the tabs and
divider; long display titles truncate within their existing row.

Validated automatic filter reset after the last simulated approval, hidden
controls for a server without reviews, and empty review bookmarks. Header row
heights and DOM nodes remain stable before/after held metadata responses.
Frontend production build/typecheck and shared-view checks passed.

Added a yellow review-count badge to the server Tools tab, shown only for a
positive count of New/Changed tools. It starts from server metadata and receives
confirmed inventory updates so approving the last tool removes it immediately.
View-as inventories do not replace the server’s administrative review count.

Changed server tab order to Configuration, Logs, Tools on request. Badge tests
confirmed the count decreases after approval and disappears after the last review.

Frontend production build/typecheck and controlled shared-view checks passed
with the reordered tabs and live review-count badge.

Made the sidebar Tools review badge a separate keyboard-accessible link to
/tools?review=1. Clicking the Tools label still opens the unfiltered inventory.
Badge positioning preserves the existing fixed-height sidebar rows.

Frontend build/typecheck passed. Controlled clicks verified that the badge opens
Needs review and the label restores all tools, with separate sibling links and
no nested anchors. Existing review/update checks also passed.

Sidebar row hover now follows the entire item, including its separate badge link,
so hovering the review badge retains the menu highlight.

Frontend build/typecheck passed; production CSS includes the parent-hover
background and text rules.

Replaced the server-detail status badge beside Actions with a dot before the
heading. It reuses serverCardStatus and the Servers-list colors/rules, including
disabled, errors/auth and pending tool reviews. Dot space is reserved while
loading; removed the superseded badge text/tone logic.

Adjusted the heading dot down one pixel for optical alignment with the name.
Frontend build/typecheck and controlled checks passed for the shared status dot,
including yellow during review and green after the last approval.

Added an Edit form to the stdio Process card for command and arguments. Arguments
are edited individually to preserve spaces and empty values; their read-only
display remains the original single joined string. Saves send minimal patches,
including [] to clear arguments, and preserve drafts with inline errors on
failure. Cancel writes nothing. Working directory remains read-only.

Frontend production build/typecheck passed. Controlled writes verified command-only
patches, exact argument values, clearing arguments, failure drafts and cancel.
No configured process or live credentials were changed during verification.

Removed the process-save restart description, endpoint-save reconnect description
and HTTP-header/secret explanatory paragraph from server Configuration.

Removed Refresh and Discover Tools from the server Actions menu, plus their
unused frontend handlers/API wrapper. Restart/Reconnect remains. The discovery
backend is retained for other consumers. Logs Refresh now uses the same outline,
small sizing, refresh icon and busy treatment as Tools Refresh.

Frontend production build/typecheck and diff checks passed for the Actions cleanup
and matching Logs Refresh button.

Replaced the Servers landing-page Refresh SVG with the same Lucide RefreshCw
icon used by Tools and Logs, retaining matching button sizing and busy behavior.

Frontend build/typecheck and diff checks passed for the refresh-icon change.

Removed the server header Actions dropdown and its enable/disable action. The
General Enabled toggle remains. Restart/Reconnect, applicable sign-in/log-out
actions and Delete are now dedicated buttons. Existing action handlers and
delete confirmation are reused, with fixed header-row geometry retained.

Frontend production build/typecheck and diff checks passed for the dedicated
server actions.

Renamed the server-header Delete action and switched it to the shared shadcn
destructive variant. Added Lucide icons to Restart/Reconnect, sign-in, log-out
and Delete buttons, retaining text labels and delete confirmation.

Frontend build/typecheck and diff checks passed for the action button styling.

Restored the official shadcn-vue dark destructive token and exact button variant
(white foreground, canonical dark opacity/hover/focus behavior), replacing the
Steam Lancache-derived pastel token and customized black foreground.

Frontend-only changes now use the existing Vite hot-reload preview on port 3000;
do not rebuild/restart mcpproxy merely to refresh embedded UI assets. Backend
restarts are reserved for backend changes or explicit requests.

Loaded the locally bundled Inter Variable font and restored the default 0.625rem
shadcn radius. Environment literals now display plainly in server Configuration;
HTTP headers keep their existing treatment. Added a narrow authenticated-admin
GET /servers/{id}/env read returning only configured env values/references with
no-store caching. Shared server lists and SSE continue using their usual redaction.
The page keeps the raw env projection locally and refreshes it after edits or
secret conversion. Secret references display a Lucide lock plus ID; conversion
buttons use that lock icon alone, retaining accessible labels.

This endpoint requires a backend rebuild/restart. Future frontend-only edits
continue to use Vite hot reload without restarting mcpproxy.

Environment literals use the same monospace size and foreground color as Process
command/arguments, removing their muted treatment. Inter is imported via Vite’s
module graph so font files are emitted and served correctly in production too.

Validation: Go suite, frontend build/typecheck, controlled env/secret rendering
and process editor checks passed. Live admin GET matched the configured env map
without printing values. Confirmed emitted Inter font files and Vite styles. One
backend restart applied the new API; subsequent typography/display changes used
hot reload only. Correction: the previous UI masked every literal, while the API
selectively masked sensitive values; raw Edit inputs exposed uncensored values.


## Replace remaining content spinners with skeletons — 2026-11-07

Added a shared delayed content skeleton and used it for Secrets, Clients, Activity,
Usage, agent tokens, recent activity and profile tool-table loading. Existing data
stays visible during refresh where available. Secrets no longer waits an extra
100 ms before making its initial request; its heading and controls remain stable.

Action-button spinners and rotating refresh icons are retained, following the
clarification that only page/content loading should use skeletons. Secrets Refresh
uses the same rotating Lucide icon as the other refreshed toolbars.

Secrets Environment lists configured ${env:NAME} references and whether they
resolve in mcpproxy’s environment. Missing filters unresolved keyring and env
references; it does not represent a separate secret store.

Frontend build/typecheck and diff checks passed. Controlled Secrets checks covered
the 150 ms delay, fast completion, retained refresh animation/data, stable heading
and Environment/Missing filters. No writes or backend restart were needed.

Simplified environment-reference labels in server settings to $NAME (for example
$HOME), preserving the original ${env:NAME} reference as the tooltip.

Made the server Delete button use the same outline base as Restart (height,
padding, border and shadow), with a solid bright Tailwind red background and
white icon/text rather than the translucent destructive fill.

Changed the server Delete button back to the same outline styling as Restart,
with destructive-colored text/icon only. Removed the filled red overrides.

Standardized requested page actions on Servers Refresh’s shared outline/sm
button styling: Clients Move/Add, Profiles Add, Secrets Add (including its empty
state), Activity Export and Settings Reload. Removed local text-xs/spacing
overrides and used consistent 16px Lucide icons. Reload retains its label during
loading, with the refresh icon rotating. Existing handlers/menus are unchanged.
Frontend production build/typecheck and diff checks passed; Vite applies these
frontend-only changes without restarting mcpproxy.

Renamed the Clients toolbar actions to “Move clients” and “Add client”, removing
the ellipses and updating the accompanying Add-client reference.

Moved Servers profile selector, search and Refresh into a separate toolbar below
the heading, with Refresh aligned right like Tools. Added an outline/sm Add server
button with a plus icon at the heading’s right edge, using the existing add route.

Frontend build/typecheck and diff checks passed for the Servers toolbar layout.
Applied through Vite without restarting the backend.


## Add servers in a dialog — 2026-11-07

Replaced the dedicated Add server page with a dialog over Servers, reusing
ManualServerForm without an extra card wrapper. Header and empty-state buttons
open it; the command palette uses the same dialog URL. Legacy /add-server and
/repositories links redirect to Servers with the dialog open. Removed the old
page and the form’s internal navigation option.

Cancel/Escape dismiss the dialog when idle; in-flight submissions cannot be
dismissed. Failures keep the form and draft open. Successful addition closes the
dialog and updates the existing Servers list without navigating to detail.

Frontend production build/typecheck and diff checks passed. Controlled DOM tests
covered opening, cancel/draft reset, legacy links, pending dismissal protection,
failure/retry and successful list update. All writes were simulated. Applied
through Vite; no backend restart.

Removed the Add server trust-toggle description. Server type choices now read
stdio with SquareTerminal and Remote with Globe, matching ServerCard icons.
Replaced the old whitespace-split argument input with the same individual fields
used in process editing, extracted as ArgumentFields. Both flows preserve exact
argument boundaries, spaces and empty strings.

Frontend build/typecheck, dialog creation checks and process editing checks passed,
including space-containing/empty arguments. All writes were simulated. Applied
through hot reload without a backend restart.

Cleaned up Add server form spacing and labels. Type options now show transport
icons first, with native radio circles visually hidden and keyboard/focus behavior
retained. Consistent grid gaps replace mixed margins and inline section headings.

Unified the env/header editor layout: aligned name/value inputs, responsive rows,
icon remove buttons and compact reveal/keyring controls. Removed duplicate value
labels and fixed top offsets from SecretToggle. Arguments use matching controls
and explicit heading spacing in both add/edit forms. Only active transport fields
are resolved/submitted, so switching types cannot create unused secret entries.

Frontend build/typecheck and diff checks passed. Controlled dialog checks covered
icon-first transport options, env/header switching, storage/reveal controls,
stable row identity after removal and exact argument/env payloads. Existing add
error/retry and process edit checks also passed. Writes were simulated; applied
through hot reload without a backend restart.

Set the Add server dialog’s width to 48rem from its initial render, clamped by
the shared viewport limit. Adding env/header rows no longer expands an intrinsic
shrink-to-fit dialog. Other dialog widths remain unchanged.

Made the Add server keyring-storage icon visibly an outline toggle button with
a stronger selected state. A wrapper tooltip remains available when the button
is disabled and explains why. The demo currently reports keychain writes are
disabled outside the tray unless MCPPROXY_KEYRING_WRITE=1; no opt-in was changed.

Increased the keyring-storage button’s gap from the value/reveal controls to 8px
so its outline no longer crowds the value field.

Removed the Clients “Other client?” card and its snippet/copy state and handler.

### Replace the customized frontend styling with current shadcn defaults

Read the official shadcn/ui and shadcn-vue agent skills, including the Vue
styling, forms, composition, icons, CLI and customization guidance. Compared the
current public demo with the registry and adopted Luma, Geist Variable and the
neutral dark palette. Removed the previous palette and most per-control color,
radius, spacing and typography overrides.

Replaced the custom sidebar with shadcn Sidebar primitives, and refreshed the
shared buttons, fields, cards, tabs, tables, menus and selection controls from
the Luma registry. Shared dialogs now use Dialog primitives; Add server uses
Field, ToggleGroup and InputGroup, and Add Secret uses Dialog and Field. Removed
custom native checkbox/switch styling and migrated callers to Reka boolean
models. Chart colors now use semantic theme tokens. Preserved compact server
cards, vivid status dots, review badge behavior, delayed content skeletons,
immediate page chrome, existing navigation and all tool-review information.

Native input value/event adapters remain to preserve existing form behavior.
Some specialized legacy overlays still use native dialogs. No backend changes
or restart were needed; the frontend preview runs through Vite.

Selected Luma instead of the initially applied Rhea style, keeping Geist and the
neutral dark palette. Reinstalled shared components from the Luma registry;
retained the input adapters that preserve existing form semantics.

Production build/typecheck and diff checks passed. Controlled DOM checks covered
dialog cancellation, pending dismissal, errors/retry, exact arguments and env
payloads; checkbox membership, radio choices and trimmed profile names; shared
tool filters, schemas, availability and snapshot-bound approvals; delayed
skeletons and process editing. All writes were simulated. Fixed missing input
type forwarding found by these checks. Applied through Vite without restarting
the backend.

Simplified the server OAuth prompt to one compact row: a Lucide key icon,
“OAuth sign-in required” and a Log in button. Removed the repeated server name,
explanation, emoji, docs link and alternate expired-session copy. Login behavior
and the action loading/disabled state remain.

Styled the compact OAuth prompt as a destructive alert with red text, key icon
and border to make the required sign-in stand out.

Moved the OAuth prompt above the server tabs so it remains visible in
Configuration, Logs and Tools. It still suppresses duplicate connection errors.

Aligned server tab content spacing by removing the extra content top margin and
omitting the alerts container when empty. All tabs use the shared gap below the
tab strip; real diagnostic alerts retain their own spacing above content.

Kept the OAuth prompt full width after reverting the fit-content width change.

Made server-header OAuth actions mutually exclusive: Sign in for signed-out or
expired/invalid sessions; Log out for an authenticated session. Removed “Sign in
again” and the connection-state checks that could hide Log out or show both.

Kept Servers text search local to the loaded list, removing per-keystroke URL
updates. Incoming search links still initialize the field. Removed the outer
scope-chip wrapper that rendered an empty row for search-only queries and
pushed server cards down; ScopeChips now owns its own visibility.

Matched server-card error text (“Needs sign-in”, “Failed to start”, etc.) to the
text-xs size used by “Review N tools”. OAuth action-state and local search DOM
checks passed, including incoming search links, no search refetches/URL writes,
no empty filter row and mutually exclusive authentication actions. Typecheck
and diff checks passed; no backend restart.

Removed the static “About MCP Sessions” explanation card from Activity.

Enlarged sidebar counts to 24px high, centered the clickable count directly
inside the sidebar badge, and matched the menu row's rounded-xl radius. Removed
the nested generic badge that introduced baseline offset and mismatched corners.

Made desktop navigation permanently expanded. Removed its collapse button, rail,
shortcut behavior and persisted collapsed-state code. Kept the mobile drawer,
its opener and closing on navigation.

On narrow screens, Servers now puts All profiles and Refresh on the first
toolbar row, with search spanning the second. Wider screens keep all three
controls on one row.

Combined Secrets into one searchable list, removing the All/Keyring/Environment/
Missing tabs and their filter state. Names now have a lock or dollar icon instead
of type labels. Missing entries sort first across both sources, then names sort
alphabetically. Kept reference strings, status and source-specific actions.

Removed Set badges from Secrets. Missing entries show a destructive badge
immediately after the name instead of beside the reference string.

Removed the missing-environment-variable summary above the Secrets list and its
unused count. Missing badges remain on individual entries.

Centered Secrets row actions vertically against the name/reference block, with
consistent 8px button gaps. Narrow rows place actions below instead of squeezing
the name beside them. Kept the standard shadcn small button sizing.

Switched Secrets Update/Remove to the default button size used by Add Secret
and server Delete, matching their height, padding and icon spacing. Removed the
temporary gap override.

For Add Value/Update on an existing secret, the dialog now focuses Secret Value
on opening. The fixed name uses the standard disabled field styling and says it
cannot be changed. New-secret creation retains its editable name and initial focus.

Changed fixed secret names to non-input text in an InputGroup surface, so they
cannot take an editing cursor. Removed the helper sentence and disabled group
styling; Secret Name and Secret Value now use identical label styling.

Added 1Password's documented page-wide data-1p-ignore attribute to the frontend
body. This opts all MCPProxy fields, including dynamic/portal secret dialogs and
server credential editors, out of its save/fill prompts.

Replaced the generic Secrets loader with skeleton rows using the current Card,
name/icon/reference geometry, responsive action layout and Luma button radii.
Kept the 150ms skeleton delay. Secret dialogs now snapshot their fixed-name mode
while open and reset on the next opening, preserving layout throughout exit;
the entered value is cleared when dialog focus teardown completes.

Added an eye toggle to the Secret Value InputGroup in create/set dialogs. It
temporarily reveals the draft and resets to masked on each opening and teardown.

Removed header display masking in KVValueCell. Added an admin-only, no-store
server headers read alongside the existing environment read, returning configured
literals/references without resolution. Header editing stays disabled until the
configured map arrives, preventing masked server projections from becoming drafts.

Frontend build/typecheck and the HTTP API suite passed. Controlled DOM checks
covered the current skeleton geometry/delay, stable closing-dialog content,
eye-toggle/reset behavior, and header load/edit/save with simulated writes.
Rebuilt and restarted only the local preview backend for the new header endpoint.

Removed reference subtext from Secrets rows and the corresponding skeleton line.
Clicking a secret name copies its canonical ${keyring:name} or ${env:NAME}
reference, excluding surrounding configured text such as a Bearer prefix.

Removed the colored outline from keyring reference badges in env/header rows,
matching the flat environment-reference badge style while retaining blue colors.

Environment reference badges now use the same Lucide dollar icon as Secrets,
followed by the variable name, instead of a literal dollar prefix.

Env/header values now render embedded keyring/env references as inline badges,
including mixed or repeated references with literal prefixes/suffixes. Editing
retains the exact original string. Convert-to-secret is hidden whenever the value
already contains a supported reference, including compound values. Confirmed
upstream connection setup already expands these compound strings.

Restored the code-style background, padding and rounded container around values
with references, keeping their badges inline inside the code element.

Limited that container to compound values; a standalone reference stays a badge.

Replaced tiny inline env/header edit/delete glyphs with right-aligned Edit and
Remove outline buttons, using the same default size, Lucide icons and red remove
text as Secrets. Narrow layouts put the action group below the value. Save and
Cancel now match input height; variable/header names align vertically with rows.

Compacted Secrets rows with 14px names, 8px vertical padding and standard xs
buttons (24px high, 12px labels); skeleton rows follow the same geometry. Server
env/header actions use the same xs buttons. Convert is now a labeled outline
button in the right-aligned group before Edit, still hidden for referenced values.

Environment-reference badges now use a yellow tint; keyring references stay blue.

Secrets list icons use matching colors: blue locks and yellow dollar signs.

Replaced the custom Alert-based toast stack with the official Luma Sonner
component and vue-sonner. Removed custom icons, palettes, animations, toast-array
state and duplicate dismissal timers. Existing notification calls delegate to
Sonner, retaining types, durations (including persistent messages), dismissal IDs,
multiline/scrollable descriptions and external-link actions.

Removed toast close controls and disabled swipe dismissal. Toasts expire using
their configured durations; existing programmatic cleanup remains available.

Production build/typecheck and DOM checks passed for the Sonner theme, duration
handling, links, multiline descriptions and programmatic cleanup, with no manual
dismissal controls. Live Vite imports load successfully; no backend restart.

Removed the remaining application-level Sonner font, description, duration and
swipe overrides, including the 60-second log preview and persistent OAuth toasts.
The Luma registry component remains untouched; only dark mode is selected for
the dark-only app. Position, size, typography, timing and dismissal use defaults,
with no close button enabled.

Removed the 150ms loading-skeleton delay on Secrets; its list skeleton now
appears immediately while initial data loads. Other pages retain their delay.

Increased server-card padding from 12px to 16px for more clearance from Luma's
rounded corners. Matching skeleton cards use the same padding and height.

Replaced the native/custom command palette with Luma CommandDialog, CommandInput,
groups, items and separators. It uses the shared Dialog overlay and backdrop blur,
registry styling/icons and Reka keyboard/focus handling. Removed the old dialog
shell, bespoke input/results styling and hand-written selection/keyboard state.
Preserved global Cmd/Ctrl+K and slash shortcuts, actions, page/directory search,
debounced tool search and stale-response guards. A non-visual input adapter lets
the existing search sources filter results without a second substring filter.

Production build/typecheck and controlled DOM checks passed for dialog blur and
focus, Ctrl/Cmd+K, slash, arrow/Enter selection, Escape/reopening, semantic tool
results, directory groups and stale-response cancellation. No API writes or
backend restart were needed.

Fixed the CommandGroup registry's heading-style marker mismatch: headings now
render the attribute targeted by the existing Luma CSS, so default muted 12px
labels and padding apply. Removed the redundant Server hint from server results.

Moved command-result hints into CommandShortcut, matching the example's native
right-hand slot and suppressing the invisible check-icon space after each hint.

### Clients and Agent tokens: finish the Luma migration

Removed Endpoint & mode from Clients because those controls already exist in
Settings. Moved its Endpoints card to the bottom of Clients using stock Card
header/content and Table composition. Removed ModeSwitcher, SerializationAxis,
their routing vocabulary and the now-unused duplicate system-store routing state
and update helpers. The shared backend routing endpoint remains used by Clients.

Fixed tab-induced page movement: Clients header actions now keep their layout
space when inactive and become invisible/inert, keeping heading/tab positions
identical across tabs, including when the toolbar wraps on narrow screens.

Replaced custom Clients warning boxes and empty state with Alert/Empty, and the
hand-built profile menu (including its keyboard handlers and lock emoji) with
DropdownMenu radio items and a Lucide lock. Profile updates and locking keep the
existing binding API and error handling.

Agent tokens now uses the stock Card/Table surface, native Empty states, a
consistent toolbar without a duplicate embedded heading, and a BaseDialog with
FieldGroup/Field controls instead of the old native dialog/custom form. Legacy
scope fields appear when selected. The one-time secret uses Card/InputGroup.
Removed painted table stripes, custom status badge colors, redundant refresh
spinner/icon and forced button colors. Corrected NativeSelect's model event type.

Production build/typecheck passed. Mocked DOM checks covered endpoint placement
and URL copying; profile changes/locking; profile-scoped and legacy token payloads;
pending-dialog protection, one-time secret copy/dismiss and failed-create drafts.
Browser checks confirmed fixed heading/tab geometry at desktop and 390px widths,
no page overflow, and the shared blurred token dialog. All writes were simulated;
no backend restart.

### Clients: consistent spacing and actual deletion

Changed the Clients tab card gap from 12px to the shared 24px page spacing.
The old Forget action only revoked a credential and retained its record, so a
manually added client could remain in the list. Replaced it with Delete: the
administrator DELETE endpoint now removes the client credential record, primary
secret, pending rotation index and matching name index in one transaction.
The shared service serializes deletion against connect/rotation, records the
change and publishes credential invalidation. Internal revoke/undo flows retain
their existing soft-revocation behavior.

Removed the old Forget result screen and its nonfunctional config-removal
checkbox. Delete closes after success and refreshes the roster in place; errors
keep the client and confirmation available for retry. Detected connection history
is retained; deleting a credential does not erase Activity.

Production build/typecheck and Go HTTP/runtime/storage checks passed. An isolated
DB test covered deleting during rotation, invalidating both secrets, removal from
the credential roster, name reuse, and missing/invalid client errors. Mocked UI
checks covered deletion failure/retry, confirmation dismissal and roster removal.
Browser measurements confirmed 24px card spacing, identical tab geometry, and no
horizontal overflow at desktop and 390px widths. No real client was deleted.
The demo was restarted once to load the backend deletion change.

### One client credential flow; authenticated MCP only

Removed the Agent tokens tab/view, standalone token REST CRUD routes, CLI token
commands, MCP credentials tool and the shared dual-kind issuance service. Removed
standalone token creation, regeneration, owner quotas/resolution and per-token
server/permission controls. Client storage/authentication now uses client-specific
names and only accepts saved `mcp_cli_` credentials. Existing client bucket keys
are retained so saved client tokens survive the change; old standalone records
are omitted from listings and their `mcp_agt_` secrets cannot authenticate.

The Clients page has no tabs. Add client uses a native Luma dialog with Name,
optional Profile and Allow switching for named profiles. It generates the client
id and returns its token once. Named profiles default switching off. All servers
always permits switching; that state is enforced by the backend and its toggle
is hidden in both creation and editing. Moving from All servers to a named
profile defaults switching off. Existing named-profile switching rules remain.

Removed client credential expiration, expiry controls/CLI flags, lease countdowns
and expiring-credential warnings/timers. Previously stored expiry timestamps no
longer invalidate client tokens. Removed the old soft Forget/revoke/undo paths;
Delete removes the client and primary/pending secrets atomically. Rotation remains.

Every MCP HTTP endpoint now requires a saved client token. Removed anonymous,
admin-key and tray bypasses on MCP, the optional require_mcp_auth toggle and
anonymous profile setting/CLI. Removed native unauthenticated stdio serving.
The dashboard REST API retains its separate administrator authentication; client
tokens are accepted only on MCP. Removed the anonymous-binding bypass guard,
its config-write checks, warning/rendering paths and remediation scaffolding.

Client lists are built only from saved credentials and their attributed sessions,
never preset client names, clientInfo names or scanned client config files.
Removed the client registry, initialize-observation discovery writes, config
credential reader/reconciler and installer claims. Reported MCP names remain
session metadata and cannot create a client. Profile references, command search,
access explanations and related links no longer offer standalone tokens or
anonymous subjects. Input secrets remain screened before issuance, credentials
remain hashed, and profile policy/tool approval still gate calls.

Validation: frontend production build/typecheck and `go test ./...` passed.
Isolated database/service tests cover creation, profile assignment/switching,
All servers normalization, persistence compatibility with old expiry fields,
old agent-token rejection, rotation deletion and id reuse. Mocked UI checks
cover creation/token delivery, no token-tab/API calls, switching defaults,
hidden/reset controls and delete failure/retry. An isolated real backend verified
all four MCP endpoints reject absent/admin/old-agent/unissued tokens while saved
client tokens initialize, that reported names cannot add clients, that All servers
creation works with immutable switching, token CRUD routes return 404 and deleting
a client invalidates its token. No real client was created or deleted for testing.
The demo was rebuilt and restarted for these backend changes.

### Compact Luma clients, profiles, activity and usage

Replaced the client table and expansion panels with small native Luma cards.
Cards show the name, profile and last use; their three-dot menus offer Edit,
View activity, Rotate token/Finish rotation and Delete. Profile and switching
changes use a focused native dialog. All servers still forces switching on and
hides the toggle; moving to a named profile defaults it off. Endpoint copies
remain at the bottom. Removed the unused legacy profile chip, inline binding
controls, credential lifecycle, warnings and admin-key upgrade components.

Rebuilt the issued-token dialog with an inline copy action and formatted,
syntax-highlighted JSON on black. Long tokens and compound config lines fit
narrow screens without horizontal scrolling. Removed the hash notice. Token
and config contents stay stable during the closing animation.

Profiles use the same compact card grid with native action menus. Creation asks
for a name and servers. The editor separates Configuration, Tools and Clients;
configuration uses native Field groups, checkboxes and selects. Tool patterns
and profile search testing stay behind deliberate expansion/dialog actions.
Rename/assign/delete dialogs use the shared controls. Existing policy omission
semantics, tool rules, switching targets, dirty drafts and failed-save recovery
remain intact.

Replaced Activity's summary/filter wall and oversized table with a compact event
list, native view tabs, inline filters and a filter popover. Input, output and
complete event metadata open in a dialog with highlighted JSON. Sessions show
client, project, status, calls and last use, with full metadata on demand.
Preserved scoped links, pagination, CSV/JSON export, parent/sub-call navigation
and live updates; background refreshes retain the current list.

Usage now has an overview card with time tabs and a timeline, followed by a
single Tools card with Calls, Responses and Errors & latency tabs. Charts use
the theme's font/colors and fit their containers. Aggregate totals, response
byte units, histogram bounds and chart links retain their existing semantics.

Validation: production frontend build/typecheck passed. Disposable mocked DOM
checks covered client creation/editing/menu actions, profile creation/rename,
policy-preserving save/error recovery, highlighted config copying, Activity
scope mapping/pagination/export/detail/session/live updates and Usage states.
Browser checks at 1440px and 390px confirmed card/editor layouts, the long-token
popup and populated charts fit without page overflow. No real client/profile
was created or changed during verification. No backend restart was needed.

### Clients list: remove search and profile filtering

Removed the profile selector, local search and their list-filtering state.
The list stays alphabetically ordered; Refresh now sits beside Add client in
the heading row. Profile assignment remains in client creation/editing, and
direct links to a particular client still work. Frontend build/typecheck passed.

### One active token per client; connection dots

Rotation now replaces the client's token in one storage transaction. The old
token stops authenticating before the replacement is returned. Removed pending
rotation fields/badges, secondary-secret authentication, overlap timers,
reconciliation, finalize/rollback storage/service methods, finalize REST route,
CLI flag and related warning/attention contracts. Older staged rotations are
completed on storage startup, keeping their replacement and deleting the obsolete
secondary index. Profile assignment and switching settings are preserved.

The confirmation says “The current token will be invalidated and replaced with
a new one.” The issued-token dialog has only Done. Client cards have a pointer
cursor and a green dot for active sessions or white when disconnected. Creating
a saved client alone no longer makes the API report it as connected.

Validation: full Go suite and frontend production build/typecheck passed. Tests
cover repeated immediate replacement, binding preservation, old-token rejection,
legacy rotation migration and deletion. Mocked UI checks cover both dot states,
cursor, confirmation, token delivery and Done without a finalize request.
An isolated real backend additionally confirmed the old token returns 401 as soon
as rotation responds, the replacement authenticates immediately, the finalize
route returns 404, and newly created clients report disconnected. The local demo
backend was restarted once to load these backend changes and checked ready.

### Client actions live in the client popup

Removed per-card three-dot menus. Clicking a client opens its profile/switching
editor with View activity, Rotate token and Delete buttons. Rotation and deletion
open their existing confirmation dialogs, closing the editor first. Revoked
clients can still open the popup for activity or deletion, with editing/rotation
disabled. Move clients is a regular heading-row button; no dropdown remains on
the Clients page. Removed unused card events, icons and dropdown imports.

Validation: frontend production build/typecheck passed. Mocked DOM checks covered
the menu-free list, Move clients, popup actions, confirmation transitions, issued
token delivery and profile saves. No backend restart or real client writes.

### Profiles: clear, mutually exclusive tool rules

Replaced the independent Allow/Deny switches with one Default/Allow/Deny selector
per tool. Choosing a rule removes the tool's opposite exact rule atomically;
Default removes both exact rules and follows the profile settings/patterns.
Broader patterns remain untouched. Matching patterns are named beside the control;
Allow is unavailable when a broader Deny pattern or an unselected server would
make it ineffective. Existing conflicting exact rules display Deny, matching the
backend's precedence. The access-level classifier remains available for tools
without annotations. Saved access is explicitly labelled, with a quiet unsaved
notice rather than stale results presented as a preview. Background evaluation
keeps the current table visible.

Maximum access uses native Luma ToggleGroup with Read, Read + write and All.
The redundant destructive/no-cap choices share All without rewriting existing
saved policy until edited. Fixed ToggleGroup's missing horizontal orientation
attribute, restoring the current registry's rounded ends and joined borders.
No theme color/radius overrides were added.

Allow/Deny pattern fields are always visible with concise wildcard/precedence
examples and explanations. Their editing buffers preserve blank lines and trailing
newlines while normalizing the saved patterns. Simplified tool names/levels,
added an empty filtered state and kept classification/access explanations.

Validation: frontend production build/typecheck and disposable mocked UI checks
passed. Covered exclusive rules, legacy conflicts, broad-pattern blockers,
classification, segmented selection, multiline editing, save payloads and failed
save recovery. Pattern hints were checked against literal/case-sensitive wildcard
edge cases including punctuation, Unicode and newlines. Browser checks confirmed
Luma rounding and fit at 320px; no real profiles were changed or backend restarted.


### Profiles: one permission checklist, shared classification

Replaced the server picker, maximum-access selector, pattern editors and per-tool
Allow/Deny controls with one grouped Permissions checklist. Server checkboxes
include current and future approved tools; individual checkboxes grant exact tool
names and exclude future tools. Quick select fills the checklist with read-only,
read/write, all listed servers, or no tools without adding another policy layer.
Permissions is the initial tab. Configuration contains only profile metadata and
switching targets; creation asks for a name and opens an empty checklist.

Added exact `selected_tools` enforcement to policy compilation, visibility, call
authorization, fingerprints, change notifications and API views. Names are literal,
not patterns. Global disablement, approval and availability checks still apply.
The UI checks backend feature support before saving exact selections so an older
backend cannot silently ignore them and broaden access.

Removed the old profile tool-table and test-search components. Existing advanced
profiles keep their saved rules on metadata-only edits. Explicit permission edits
convert saved effective visibility to exact selections, preserve inherited code
execution restrictions, and require an explicit choice for unavailable legacy
servers. Legacy configuration/CLI compatibility remains; opening a profile does
not rewrite its permissions.

Moved classification of unannotated tools into shared Tools details, including the
server Tools tab. Manual classifications are stored separately from upstream
annotations, apply across profiles, and expire when the tool definition changes.
Writes require administrator access, the current definition hash and server
incarnation. Classification does not approve or enable a tool. Successful changes
notify connected clients that tool visibility may have changed.

Validation: frontend production build/typecheck, disposable mocked UI checks,
Go policy/config/storage/authorization tests, and isolated HTTP/MCP integration
passed. Coverage includes exact-name grants, hidden/uncallable unselected tools,
legacy metadata preservation and conservative conversion, failed-save recovery,
shared classification, stale-definition rejection and name-only creation. The
local demo backend was restarted once for the new authorization/API behavior;
frontend iterations use Vite without further backend restarts.


### Development-only model: removed compatibility and migrations

Removed the old profile tier cap, unannotated handling, allow/deny patterns and
per-profile classifications from config, policy compilation, REST/MCP schemas,
CLI flags, access explanations and frontend types. Deleted the conversion notice,
legacy draft preservation and conversion guards. Profiles now use only server
selection and exact tool selections. Removed the deprecated profile tool-count
field and its extra server-list scans. Unannotated tools count as visible when
selected, without an implicit tier restriction.

Removed token-rotation and session-bucket startup migrations, historical tool
approval hash formulas/backfills and collapsed-name approval inheritance. Removed
the obsolete server baseline marker/inference subsystem, index-format upgrade and
rebuild machinery, host-wide instance-ID adoption, old OAuth token-key lookups,
activity metadata fallbacks and deprecated config cleanup. Client credentials use
their own current buckets; old agent-token buckets are not adopted. Configuration
rejects unknown fields and invalid values instead of rewriting them. No migrations
or preservation paths replace the removed code.

Removed the old active-profile REST API, MCP endpoint aliases, old frontend page
redirects, `call tool`, `upstream approve`, `secrets migrate`, `--risk` and `--agent`
compatibility aliases, bulk-secret migration analysis, and anonymous administrator
contexts. Removed older-controller fallbacks around tool mutations and made
review hashes mandatory for REST approvals. Removed backend/frontend feature
negotiation and status waits for scope filters. Removed historical schema
constants and unused compatibility wrappers. AGENTS.md prohibits new compatibility
shims, deprecated aliases, format fallbacks and migrations during development.
Third-party MCP/OAuth protocol interoperability remains supported.

Tool tier editing now uses the existing badge. Unannotated and manually assigned
tiers have a brighter dashed outline with no fill and open a native Luma radio
menu. Server-provided annotations remain static badges; Write is yellow and
Destructive is red. Removed the separate Access level field and Shared across
profiles caption. View calls now uses a Lucide SVG arrow instead of a Unicode
character that iOS rendered as an emoji.

Profile capability toggles are off and disabled when the global setting blocks
them; read-only mode also locks server management. Only locked controls show a
short Disabled globally explanation. Globally enabled capabilities remain
switchable per profile, and locks do not rewrite saved preferences.

Validation covers strict removed-field rejection, exact tool visibility and call
denial, mandatory review hashes, saved-client-only MCP authentication, approval
changes and exact reverts without formula conversion, definition-bound manual
tiers, stable local badge updates, native menu selection, the SVG arrow and
per-profile capability locks. Full Go tests, frontend production build/typecheck,
isolated HTTP/MCP integration and disposable mocked DOM checks were used.

Applied the backend changes to the disposable local demo with one restart. Removed
the unused features field from that demo config for strict validation; the main
instance and its configuration were untouched. The demo uses the current client
credential namespace without importing old agent-token records. Servers, Clients
and Profiles endpoints returned 200 after startup.

### Quiet profile editing

Removed informational warning banners from the profile editor and the backend
warning for an empty server selection. Empty profiles are a normal supported
state. Validation errors and the reload notice for concurrent edits remain.

### Profile heading actions

Removed the profile editor's three-dot menu. Delete is an outlined red-text
button beside Save, matching server deletion actions. Rename is a pencil-only
button beside the heading, with an accessible name. Both use the existing
confirmation/edit dialogs and are disabled during saves or renames.

### Client dialogs and server configuration

Client Save is disabled until the selected profile or switching setting changes;
reverting those changes disables it again. Rotation, its new-token display and
Delete confirmation share the client editor's mounted dialog and backdrop,
preventing the blur from flashing between popups. Extracted the shared token and
highlighted config fields and removed the separate delete dialog and parent
rotation state.

The server General card has Enabled and Trust all tools before Protocol. Removed
the redundant Name row. Protocol uses the same globe or terminal icon as server
cards, with Remote or stdio text instead of an inline code badge.

### One profile name and one editor page

Removed profile display names from config, API responses, client presence, MCP
management schema, CLI flags and frontend selectors. A profile has one name and
an optional description. Removed the profile editor's tabs and client assignment
section; Description, tool permissions, capabilities and switching targets now
share one page. Client assignment remains available from the Clients page and
profile list. No migration or compatibility alias is added.

Validation: frontend production build/typecheck, full Go suite, removed-title
config rejection, and mocked DOM checks for profile description saves and
client dialog transitions passed. No backend restart for these UI changes.

### Remove profile descriptions

Removed the optional profile description from the editor, profile search and
card tooltips, stored config, API views, MCP management schema, change diffs,
and CLI creation/update/clear/preview flags and output. Profiles now have only
one name plus their permission settings. Removed config fields are rejected;
no migration or compatibility handling is added.

Removed search from the Profiles page, including local filtering state and the
no-matches empty state. Profiles remain sorted alphabetically.

Removed trailing profile and client hints from Cmd+K results.

### One MCP endpoint

All clients use `/mcp` with the global routing mode. Removed the dedicated
direct, code execution, retrieve and profile URL mounts; their paths return
404 rather than falling through to the default MCP handler. Removed profile
URL middleware, context injection and URL precedence from profile resolution.
Profiles are selected from client credentials and permitted session switching.
Removed endpoint maps from routing metadata and CLI status/doctor output for
the discarded modes. Removed the Clients Endpoints card, its copy helpers,
the extra routing fetch and the duplicate routing payload in client lists.
Routing mode changes keep the existing restart requirement.

Validation: frontend build/typecheck and full Go tests passed. Isolated real
HTTP/MCP checks verified each global routing mode on `/mcp`, 404 responses for
discarded routes, client-token authentication, profile-selected tool discovery
and calls, and absence of unused endpoint/routing metadata.

Applied the backend to the disposable demo with one restart. Its status, Clients
and routing API returned 200; each removed MCP route returned 404. Removed
obsolete profile labels from the demo configuration for the current strict
schema. The main instance was not changed.

### Readable tool diffs

Changed tools now show unified diffs for descriptions and input/output schemas:
red minus lines, green plus lines, inline word highlights for description
edits, syntax-highlighted JSON and expandable unchanged context. No line
numbers. Removed the raw Changes since approval and Previously approved
definition sections and the standalone Annotations section. Changes to safety
hints appear as readable behavior changes. Current schemas remain available.
JSON highlighting is shared with the existing schema viewer.

Replaced the handwritten diff's size-limit replacement fallback with jsdiff's
Myers algorithm. On the real Easyship update_shipment schema this reduces the
incorrect 615 removed / 819 added lines to 1 removed / 205 added, retaining
808 unchanged lines as context. The additions are five new address fields
with nested definitions; the removed line is a changed parameter description.

Expanded tool content now scrolls within a viewport-relative height limit,
keeping the tool name and approval controls outside the scrollable region.
Descriptions, diffs and schemas share this bounded area. It supports keyboard
scrolling and prevents scroll chaining from moving the tool header away.

### Port logo

Replaced the old sidebar logo and favicon with the selected monochrome Port
vector. The sidebar uses white strokes for the dark interface; the favicon
adapts to the browser's light/dark color scheme. The favicon URL respects
the `/ui/` base path and changes its cache key to replace the previous icon.

Server headers in the Tools list now stay sticky on small screens, retaining
the server name and bulk approval button while scrolling that server's tools.
They sit below the mobile navigation button and stop at the section boundary.
The tool card uses overflow clipping without becoming a scroll container, so
sticky positioning follows the page scroller. Desktop headers remain static.

### Faster local review filtering and HTTP-safe toasts

Needs review still expands matching tools, but offscreen detail bodies mount
only near the viewport, using one shared observer that is cleaned up on
unmount. Descriptions and review diffs are prepared one tool per background
task, with Changed tools first, and cached for expansion. Review snapshots
use a WeakMap; rendered Markdown uses a bounded cache. Filtering stays local.

Removed crypto.randomUUID from toast creation. It is unavailable on ordinary
HTTP LAN origins used by mobile Safari; Sonner now generates its own IDs,
and dismissal preserves their native type. Toast styling is unchanged.

Validation: production build, 11 real diff fixtures, cached/chunked warmup,
302 automatic review expansions with only six nearby descriptions mounted,
viewport activation and observer cleanup, no filter refetch, and toast
creation/dismissal with randomUUID absent. No physical Safari test was run.

Missing environment variables in Secrets now show the italic instruction
“Add this variable to the MCPProxy environment”. Removed the How to Set
button and its instruction-toast handler.

### Mobile copying and server configuration rows

Secret references and other Copy actions now share a clipboard helper. On
HTTP LAN origins it selects and copies synchronously within the user's tap,
with an explicit selection range for iOS, no keyboard, and focus/selection
cleanup. It checks the copy result instead of reporting false success.
Dialog copies keep the temporary selection inside the active dialog.

Moved Connection Edit into its header action, matching Process. Server
headers and environment variables now use responsive name/value lists: the
name sits above the value on narrow screens and alongside it on wider ones.
Add forms are outside the list's columns, with full-width mobile inputs and
a separate action row. Existing value editors also stack controls on mobile.

Validation: production build, exact keyring/env reference copies without
the Clipboard API, native API and failure cases, cleanup and dialog focus,
actual Secrets copy actions, Connection header action, and both responsive
add forms. No physical Safari device was used.

Header and environment row/form layouts now use explicit single-column defaults
and card-width container queries. Names stay above values until the card
content itself has enough width for columns; the value editor and its actions
also respond to available card space instead of the screen breakpoint.

Aligned stacked environment/header names with the inset text of their value
blocks. The wide two-column layout keeps its existing label alignment.

Reference badges in header/environment values now wrap long names within the
available width and grow in height, including inside compound code values.
The reference icons retain their size; global badge styling is unchanged.

### Compact server errors

Server errors now use the same compact destructive alert styling as OAuth
sign-in: one small Lucide icon and the error message. Removed the generic
heading, severity/code badges, separate cause, troubleshooting steps, commands,
fix execution controls and documentation links. Structured and plain errors
share the same component. Removed its unused frontend fixer API wrapper,
response/step types and refresh callback. Backend diagnostics remain available
to the CLI.

Process-exit errors now say “The process failed”, with captured stderr shown
beneath in regular-weight text. Removed the connection wrapper and generated
troubleshooting hint from that stderr display; no additional actions or
expanders were added.

### Conventional mobile sidebar trigger

Moved the stock shadcn SidebarTrigger into a normal mobile header within
SidebarInset, alongside the Port wordmark. Removed absolute positioning and
the compensating page padding. The header stays outside the page scroller;
sticky tool-server headers now anchor directly below it. Desktop navigation
remains permanently open and has no extra header.

### Mobile refresh and sidebar gestures

Mobile pages now scroll the document instead of a locked viewport with an
inner page scroller, restoring the browser's native pull-to-refresh path.
Desktop retains the existing viewport layout. The mobile header remains
sticky, as do server review headers beneath it. Detail frames allow mobile
scroll chaining and retain containment on desktop.

A rightward swipe opens the native sidebar Sheet. Gestures starting within
32px of either edge, in horizontal scrollers or form/slider controls, as well
as vertical, leftward, multitouch and text-selection gestures are ignored.
Listeners are passive and never prevent native scrolling or browser navigation.

### Profile cards without action menus

Removed the card menu and its unused list-page rename/delete/assignment dialog
state. Deleted the now-unused assignment dialog and removed the redundant
client roster fetch from Profiles. Edit, rename and delete remain in the
profile editor; client assignments are managed in Clients.

### Consistent deletion dialogs

Server, profile, secret, header and environment-variable deletion now share
one confirmation dialog with the server dialog's title, emphasized resource
name, irreversible-action wording, outlined Cancel and destructive Delete
button. Secret deletion correctly names the keyring. Client deletion uses
the same message and action styling within its existing mounted dialog,
preserving the backdrop across the edit-to-delete transition. Removed native
confirm calls for secret and server-value deletion. Busy states, profile
reassignment guards and inline errors remain; closing animations retain their
resource labels.

Validation: production build, guarded mobile swipe interactions, drawer
navigation, native document-scroll layout, profile-menu removal, all six
delete confirmation types, no writes on Cancel, stable client backdrop, and
mocked secret/header/environment deletion requests. No physical iOS device
was used for the native pull-to-refresh gesture.

### Dark background before frontend startup

Added inline document background styles and dark browser color-scheme/theme
metadata so refresh and native pull-to-refresh retain the app's dark canvas
before the main stylesheet and Vue load. No loading overlay or backend restart.

Validation: production build passed; generated HTML retains the inline dark
background before the frontend scripts and stylesheet load.


### Sidebar swipe without logo selection

Mobile drawer opening now focuses the drawer instead of automatically focusing
and outlining the logo link. Logo dragging and sidebar text selection are
disabled; keyboard focus indicators on navigation links remain intact.

Validation: production build and mocked mobile swipe checks passed, including
focus landing on the drawer rather than the logo and existing gesture guards.

### Dead-code audit and daemon-only cleanup

Removed the management CLI and its HTTP client, output formatters, command
helpers and upstream CLI wrapper. The executable retains `serve`, startup
flags, help and `--version`. Removed abandoned application-context adapters,
repository guessing, discovered-client identity handling, the duplicate server
actor, unused health adapters, stale converters and uncalled service wrappers.

Removed orphan HTTP routes for the old attention bar, onboarding wizard,
per-server review page, draft-profile search, diagnostics/fixers, call-history
and replay screens, standalone code execution, bulk controls and other removed
surfaces. The attention subscriber, timers, persisted onboarding state, scanner
baseline remnants, troubleshooting payloads and placeholder fixers are gone.
Current MCP tools and the unified Tools, Activity, Usage and Settings flows stay.

Removed the tray socket, listener multiplexer, OS-credential admin bypass,
tray/installer provenance, tray assets and installer script. The daemon has one
HTTP/HTTPS listener and does not serve MCP over stdio. Upstream stdio servers
remain supported. Removed the socket config/flag and retired CLI/tray activity
sources. Linux and macOS are the supported platforms.

Removed unused frontend components, stores, API methods, utilities and type
contracts; anonymous/token subject and binding-guard UI left behind by earlier
removals; client credential purpose; duplicate font download; unused ESLint
packages and its skipped-lint placeholder. Authentication uses the native
shadcn dialog instead of custom native-dialog/focus-trap composables. Simplified
build/development instructions and targets to match the remaining application.

Removed orphaned fault-injection hooks, waiter probes, test counters, no-fsync
switches, obsolete QA artifacts and their Playwright test. Retained 13 focused
regression tests in 11 files for behavior that still exists, including client
token replacement/deletion, exact profile selections, current tool approvals,
output schemas, and administrative server-value reads. No new persistent test
framework or compatibility/migration code was added.

Validation: Go tests and vet, production frontend build with unused-local checks,
mocked mobile sidebar and deletion flows, and isolated real HTTP/HTTPS daemon
smoke checks passed. The smoke checks covered API-key authentication, client-only
MCP authentication, immediate token rotation, deletion, removed-route 404s, no
IPC socket, and rejection of removed CLI/socket flags and empty-listen stdio
requests. The final native Go reachability scan reports zero unreachable
functions. No main-instance configuration or credentials were changed.

Comparison baseline: `7133c709e6f07473c3271c9d6e3732f4073d84b8`
(Dependabot, 2026-10-16)

## Optional upstream server icons — 2026-11-08

- Added SVG/PNG editing to the icon beside the server heading: a Replace/Remove
  menu for an existing icon, or a dashed upload placeholder when none is set.
  Icons live in the server config; no separate asset store or migration.
- Icons resolve as uploaded override → upstream `serverInfo.icons` artwork → no
  icon. Upstream artwork is validated and cached in memory per connection, with
  no credentials sent to image URLs. Removing an upload restores upstream artwork.
- Reused one icon component beside server names on cards, server headings, tool
  sections, profile permissions, Activity, filter chips, and command search.
- Added standard MCP tool icons for direct tools, per-upstream `_meta.source`
  logos on tool results, and a server-icon map in discovery/description metadata.
  Public, content-addressed image URLs let clients load logos without credentials.
- Restricted uploads to small PNGs or static SVGs without scripts, HTML, external
  resources, or stylesheets. PNGs are decoded and dimension-checked. Replacing an
  icon does not reconnect its upstream.
- PNG artwork has transparent borders trimmed once on upload/upstream import,
  preserving aspect ratio and partially transparent edge pixels. Cropped content
  gets its own image URL and is also exposed in caller metadata.
- Validated with Go tests/vet, frontend build, isolated DOM upload/removal checks,
  and real daemon smoke checks in direct and retrieval modes.

## Central management and lightweight peers — 2026-11-08

- Added one primary/peer model. The primary owns configuration, profiles,
  client tokens, approvals, tool indexing, and activity/usage. Peers execute
  assigned MCPs and expose only a local `/mcp` endpoint.
- Added `mcpproxy peer` with URL/token flags and environment variables, an
  outbound authenticated control stream, immediate token replacement, reconnects,
  cancellation, and authoritative configuration synchronization.
- Added host-bound clients and host-aware visibility: local servers plus explicitly
  shared primary servers, intersected with the same central profile. No nested
  proxy tools, duplicate profiles, peer-to-peer forwarding, or offline policy cache.
- Kept keyring secrets, environment resolution, OAuth tokens/code exchange, and
  refresh scheduling on the execution host. Central management relays secret
  writes, sign-in callbacks, and log reads. Local identity stamps fence stale
  OAuth callbacks after deletion or recreation; peer configuration is not stored.
- Added compact native Luma Peers cards and dialogs, server/client host selectors,
  primary-server sharing, source labels, host-scoped Secrets, and neutral offline
  states. Activity records retain the calling host and actual execution host.
- Removed a stale race-build initialization file that referenced an already
  deleted variable and prevented race-enabled server builds.
- Fixed OAuth callback delivery when a redirect arrives before its waiting
  goroutine starts, while retaining single-use state and replay rejection.
- Validated with Go tests/vet and race checks, a Linux build, the production
  frontend build, mocked DOM interaction checks, and isolated primary/peer
  processes covering host/profile scoping, execution, activity, secrets,
  offline edits/reconnects, credential rotation, OAuth exchange/refresh/logout,
  and token locality. Demo updated; main instance untouched; no commit made.

### Peer management in Clients

- Removed the separate Peers page, route, and sidebar item. Clients now has
  a Primary section and one section per peer, including peers with no clients.
- Moved Add peer and the existing pairing/rotation/deletion dialog into Clients.
  Each section has Add client with its host preselected; peer sections also
  have Manage peer. Removed repeated host labels from individual client cards.
- Frontend build and mocked DOM flows passed. Updated through Vite without
  restarting the backend.
- With no peers, Clients stays a flat list with Add client only in the top bar.
  Registering a peer enables host section headers and per-section Add client,
  hiding the global button. Primary has no status dot.
- Hide Connect through and Run on in client/server creation and editing until
  at least one peer exists. Load peer availability independently of the hidden
  selectors, using the shared peer-store condition.

### Central credential storage

- Replaced the initial peer-local credential model above. Keyring secrets are
  now global on the primary: one reference can serve primary and peer servers.
  Peers receive only the secrets referenced by their assigned configurations.
  Environment references still resolve on the execution host.
- Removed peer secret CRUD/relay operations and the Secrets host selector.
  The global inventory includes unreferenced stored secrets; environment entries
  retain their execution host and distinguish offline from missing values.
- OAuth grants and client registrations persist on the primary. The execution
  host performs discovery, sign-in, code exchange, and token refresh. The primary
  excludes peer-owned grants from its refresh scheduler.
- Removed the peer credential database. Peers hold credentials and ordered
  pending updates in RAM, with acknowledged revisions, replay-safe mutations,
  and process-specific writer leases. Restarted peers reload central grants.
  No import or migration of former peer-local credentials was added.
- Logout revokes the lease centrally even while a peer is offline. Old processes,
  late token rotations, and delayed configuration snapshots cannot restore the
  revoked grant. Losing a peer process before a pending rotation is saved can
  require signing in again.
- Validated with Go tests/vet and race checks, a Linux build, frontend typecheck
  and production build, mocked DOM flows, and isolated primary/peer processes.
  Checks cover ordered rotations and lost acknowledgements, grant recovery after
  restart, offline logout and subsequent sign-in, global secret inventory,
  execution-host environment references, and absence of a peer credential DB.

### Secret usage links and client cleanup

- Secret rows show their configured server usage before the action buttons:
  one server links directly to Configuration, while multiple servers use a
  compact badge and dropdown. References within compound values are included;
  repeated references count once and environment usage stays host-specific.
- Removed Move clients, its bulk-move dialog, frontend API/types, and dedicated
  REST endpoint/handler. The shared bulk-assignment service remains used by
  the MCP profile-management tool.
- Disconnected peer sections show lowercase “last seen …” only when they have
  connected before. Online and never-connected peers show no status text.
- Run on and Connect through selectors show peer names only. Missing environment
  instructions appear before their server usage badge.
- Removed the redundant action from sign-in toasts. Restart success now appears
  only after reconnection, as “Server restarted” without a conflicting subtitle.
- Profile deletion now suppresses its own invalidation reload until navigation
  back to Profiles, preventing an early “Profile not found” from unmounting the
  confirmation dialog. Empty Clients and Profiles lists use one compact dashed
  creation card in the normal card grid, including per-peer client sections.
- Client identities are now assigned by the backend. Removed the ID field from
  the create request and all browser ID generation, eliminating the secure-context
  randomUUID failure on LAN HTTP. The browser sends only the client name and
  its host/profile settings; the backend creates the identity and token together.
- Audited the other creation flows: peers already receive backend IDs; servers,
  profiles, and secrets use user-entered names. No other browser-generated entity
  IDs remain. Framework DOM IDs and the Settings API-key generator are unrelated
  to entity identity.
- Creation cards remain at the end of populated client/profile grids. Client cards say “Add a <host> client” when peers exist, otherwise “Add a client”; profiles say “Add a profile”.
- Servers no longer repeat the selected profile in a scope badge. Empty profiles and unmatched searches share “No servers found” and Clear filters, with no subtitle.
- Dashed creation cards center their icon and label vertically and horizontally, including when adjacent cards stretch the grid row.
- Header/environment editing now includes the name. Renames remove the old key and save the new name/value in one merge patch; duplicate names are rejected, including case-insensitive header collisions.
- Hide Available to peers when no peers exist and sharing is off. Previously enabled sharing stays visible so it can be disabled, then hides until a peer exists again. Applies to server configuration and creation.

### One log format and a live viewer

- Standardized file and console logging on newline-delimited JSON with recorded
  RFC3339 timestamps and fractional seconds. Removed the format configuration
  field and Settings toggle, console encoder/parser, unused CLI logger variant,
  and its permanently false core-client option. No old-format reader or log
  conversion was added.
- Fixed the reader inventing a current timestamp for every unrecognized line.
  It now accepts only the writer's JSON records, keeps their timestamp/level,
  scrubs message and structured fields, and skips malformed or partial records.
- Replaced raw JSON blocks with timestamp/level/message rows and expandable,
  highlighted fields. Added local search/level filters, pause/resume, manual
  refresh, and live updates every 1.5 seconds. Background updates preserve row
  expansion and scroll position, without loading animations; hidden tabs and
  navigation stop polling. Jump to latest resumes following the bottom.
- Shared a bounded tail reader between primary and peers that scans from the
  end instead of loading the entire file on every refresh. Disabled-server logs
  remain readable. Removed the unimplemented management-service log method.
- Removed extra content margins in configuration cards and aligned header
  heights so General and Connection/Process start their rows consistently.
- Validated writer/reader round trips, stable timestamps, malformed/old-format
  rejection, redaction, long lines, growing/truncated files, frontend build,
  live DOM interactions, real primary/peer logging and captured stderr, Go
  tests/vet and race checks. Main instance untouched; no commit made.
- Server Configuration places General beside Connection/Process at the xl breakpoint, retaining the stacked mobile layout and full-width credential cards below.
- Simplified the live log viewer to newest-first, permanently live updates with
  a green Live indicator, and inline error text. Removed pause/refresh controls.
  Reading older rows holds them steady while buffering newer entries; Jump to
  latest returns to the top. Remaining metadata stays expandable.
- Removed log level filtering. Log rows expand on click while preserving drag/word selection, copying, and interaction with expanded metadata; the chevron remains keyboard accessible.
- Activity no longer offers an Unattributed client filter. Client chips, table
  cells, and sessions resolve saved names from the full client roster instead of
  showing IDs or preferring self-reported harness names. Verified `/mcp` rejects
  anonymous and admin-key requests without a saved client token.
- Servers hides the profile selector when no profiles exist; search and Refresh
  stay on the same row, and the selector returns after a profile is created.
- Activity labels records without a named profile as All servers, rather than
  Unattributed. Tools preview no longer repeats its selected client/profile in
  separate chips, appends duplicate dropdown options, or labels clients by ID.
  Client/profile preview choices remain mutually exclusive; interaction checks
  cover switching subjects and clearing the previous selection.
- Removed the wide Tools preview alert and redundant leave/count text. A native Read-only badge sits beside the View as selector; unused preview-count state was removed.
- View as is the selector’s default option instead of Administrator; removed the external label.
- Removed the redundant Profile tier display from expanded tool details in access preview.
- Access explanations resolve client names in headings, verdicts, and suggested
  actions. Renamed Credential to Client token and Server state to Server
  connection, with explicit pass descriptions. Verified the reported demo pair:
  server-a was connected; server-b was OAuth-blocked and failed that check.
- The approval suggestion now says Review tool and opens the correct server/tool
  in review mode, clearing preview scope. It no longer treats a server:tool pair
  as a server filter. Interaction checks cover the complete navigation.
- Removed scroll containment and the extra scroll container around tool cards;
  bounded expanded details can hand scrolling back to the page at their edges.
- Removed What you can do from access explanations, its frontend navigation,
  backend suggestion generator, response fields, and unused action constants.
  Check badges use native theme variants and Lucide icons in one fixed-width
  grid column; check names align and descriptions sit underneath.
- Compacted access-check badges to a 4rem column; skipped checks use the shorter Skip label.
- Removed the redundant client-token access-preview check from the evaluator,
  API step vocabulary, UI, subject overrides, and associated state/clock helpers.
  Saved-client lookup and HTTP token authentication remain prerequisites; access
  explanations now contain only the five actual policy/readiness checks.
- Profile deletion hides the move label and selector when no destination profiles exist, retaining the explanatory notice.
- Client cards show the profile at the top right and missing-profile error text at the bottom right, with a red status dot and no error badge or extra content row.

- Profiles now have backend-generated stable IDs and a single editable display name (spaces and Unicode allowed). Bindings, switch targets, routes and policy/index lookups use IDs; names are shown throughout the UI. Renaming no longer rewrites client tokens or live sessions. Removed slug restrictions, reserved profile names and rename reference-rewrite machinery. No compatibility conversion or migration.

- Access explanations name the subject once, show the assigned profile only for client checks, and omit internal profile-source labels such as `none` or `pin`.
- Client cards hide the profile name when its assignment is missing, retaining the red status dot and error text without duplicating “Profile missing”.
- Access explanations describe a deleted client profile as “using a missing profile”, based on the failed profile check.
- Simplified access-check failures: one approval message for New/Changed tools, no internal status codes or passing subtexts, and plain client-availability wording. Server lookup failures now distinguish a missing server from a storage read error.
- Replaced the entire access-explanation checklist with Available/Unavailable and concrete blockers only, using native alerts. Removed pass/fail steps, check-name/status enums, first-failure and profile-source payloads, custom verdict colors, and cascading profile errors from the backend and frontend. Existing permission and review rules remain enforced.
- Profile editing includes a client-assignment card beside the server/tool list. Checkboxes share Save/Discard with permissions, use existing binding APIs, and return unchecked clients to All servers. Successful assignments remain saved if a later assignment fails; remaining changes can be retried.
- Restored desktop sidebar collapsing with the native trigger and rail, an icon-only collapsed state, saved open/closed preference, and Cmd/Ctrl+B. Removed the desktop-collapse disable flag; mobile drawer behavior is unchanged.
- Sidebar count badges stay hidden during the desktop expansion animation, then fade into their final position instead of visibly moving with the sidebar.
- Wide touch screens use document scrolling rather than the fixed desktop content scroller, restoring native browser pull-to-refresh without changing desktop navigation. The collapsed sidebar trigger now shares the menu icons’ left alignment.
- Sidebar toggle follows the animated right edge instead of switching alignment. Branding stays out of flex sizing, keeps the logo fixed-size, and fades in after expansion settles to avoid snapping and squeezing.
- Dialogs focus the dialog container when their first control is a select, preventing mobile pickers from opening automatically. Normal text-field focus and explicit secret-value focus are preserved.
- Profile editor loads its profile, permissions and capabilities in parallel and applies the saved form and draft together, preventing transient empty values and false unsaved-change flashes. Heading actions remain in place while loading.
- Removed Prometheus export and HTTP profiling: scrape/profiling routes, collectors, exporter configuration, event bridge, OAuth-refresh recorder, tool-call metrics, rejection sinks and unused limiter sampling APIs. Removed unnecessary dependencies, including Chi’s middleware import that pulled in the HTTP profiler; retained request cancellation and panic recovery. Refreshed tracing documentation. UI Activity/Usage, tracing, health probes and the authenticated live event stream remain.

- MCP administration is now a client permission, off by default, checked from current storage on every management call/list. Trusted clients can manage servers, clients, profiles, peers, secrets, tool reviews/classification, settings, activity/usage/export and prompt approvals through MCP in every routing mode. Ordinary tool calls retain their profile/host scope; client tokens still cannot access REST. Dashboard OIDC remains a separate future change.
- Replaced the old server/quarantine MCP handlers and JSON-string arguments with typed domain tools that reuse dashboard validation, services and writes. Removed their duplicate config writer, connection monitor, patch/redaction wrappers and obsolete profile/global management switches. Approvals retain reviewed hashes/incarnation checks; management activity omits secret/token payloads. Verified the peer listener exposes only `/mcp`, forwards client authentication with its own peer credential, and leaves client/host validation to the primary. No compatibility aliases or migrations.

- Moved code-execution permission entirely to clients alongside MCPProxy management, with independent persisted toggles and live authorization. Removed the global enable switch, profile code-execution configuration/effective fields/policy hashes, capability UI/fetches, global hot-reload machinery and obsolete denial types. Scripts retain ordinary profile/host/tool restrictions. Both client permissions default off and survive token rotation; no migrations or old setting aliases.

- Replaced dashboard admin API keys with generic OIDC browser sign-in: provider discovery, authorization code + S256 PKCE, single-use browser-bound state, nonce and signed ID-token validation. Dashboard administrators are users admitted by the configured OIDC application. Added opaque in-memory sessions, HttpOnly/Secure/host-prefixed cookies on HTTPS, CSRF-protected writes, sign-out and session-bound event-stream cancellation. Client/peer credentials remain MCP-only; an unconfigured dashboard fails closed.
- Removed key generation/persistence/banner/config overrides, key-bearing URLs/headers/localStorage handling, the key prompt, and dedicated admin-key log-redaction/input-screen paths. The UI uses cookie sessions for requests, events and exports. Unauthenticated document visits redirect directly to the provider before loading the frontend; expired UI sessions redirect too, without a login splash or button. Documented Pocket ID/generic provider registration, deployment settings and session behavior. No legacy key fallback or migration.

- Removed redundant health aliases (`/health`, `/livez`, `/ready`) and their duplicate registration, leaving `/healthz` and `/readyz`. Removed bare-path dashboard redirects and their obsolete page-name allowlist; dashboard routes use `/ui/`, with only the normal root entry redirect retained.

- UI bundles, CSS and fonts now require a dashboard session, in both embedded serving and the Vite demo. Unauthenticated assets return 401; document navigations keep the immediate OIDC redirect. Only the favicon and content-addressed server icons remain public among UI/image assets. Authenticated UI responses use private cache revalidation.

- Removed built-in setup/troubleshooting MCP prompt templates and their config, process flag and settings toggle. Upstream prompt aggregation and its review/scope checks remain; disabling aggregation clears published prompts.

- Management tools now use the built-in `mcpproxy` namespace and the normal tool discovery/calling flow. Retrieval/code-execution clients no longer receive administration schemas in their initial tool list; definitions are searched with the same BM25 implementation and support compact/full retrieval and schema lookup. Scripts require both management and code-execution permissions; management payloads are omitted from execution history. Direct mode follows its normal full/deferred tool presentation.

- Removed MCP prompts entirely: upstream listing/fetching and notifications, aggregation and scope filters, prompt approvals and storage APIs/bucket creation, refresh events/debouncing, prompt activity records, per-server/global config and process flags, and `manage_prompts`. MCPProxy no longer advertises the prompts capability; there is no prompt compatibility layer or migration.

- Removed the anonymous OIDC pending-flow table: sign-in data is now carried in an expiring authenticated-encrypted browser cookie, with replay records created only after verified sign-in. Anonymous login/page requests cannot consume the global pending-login quota.
- Peer OAuth callbacks require a single-use state registered by the authenticated management flow, bound to the server and peer, before any relay; invalid public callbacks return 404 without contacting peers. Local callbacks require a pending state as well.
- All upstream MCP HTTP/SSE clients reject cross-origin redirects before transport wrappers can reattach credentials (application mitigation for GO-2026-6911; the SDK advisory still has no fixed upstream version).
- Updated the project toolchains to Go 1.27.3 and Node 26.13.0/npm 12.2.2, MCP SDK v2.0.0 and the other Go build dependencies and frontend packages to their latest stable releases, with TypeScript 6.0.3 required by the current Vue type checker (TypeScript 7 lacks the JS compiler API it needs). Updated Docker build/runtime stages. Kept the unmodified shadcn-vue 2.9.0 stylesheet alongside checked-in components and removed the generator CLI dependency. Latest compatible selector parsing and DOMPurify overrides remove the remaining npm audit findings.

- Added `Content-Security-Policy: frame-ancestors 'none'` to embedded and Vite UI responses to prevent dashboard framing.

## Encrypted primary storage

- Removed OS keyring providers, desktop-session availability checks, keyring write flags and the keyring dependency.
- Replaced the primary BoltDB store with a single encrypted transactional Badger database at `primary.db/`. Named secrets, OAuth grants/client registrations, client and peer credential records, and other persisted application records use this store. No migration or old database reader remains.
- Require one deployment-provided base64 32-byte master key through `MCPPROXY_ENCRYPTION_KEY` or `MCPPROXY_ENCRYPTION_KEY_FILE`. Missing, invalid or wrong keys fail startup. Derive the client-token HMAC key from it instead of generating a plaintext `.token_key` file.
- Replaced `${keyring:name}` references with `${secret:name}` throughout current code and UI. Reject obsolete providers. Compound values discover every reference and never recursively expand secret contents.
- Peers remain credential-storage-free: only in-memory secret/OAuth snapshots supplied by the primary. Added `--peer-token-file` / `MCPPROXY_PEER_TOKEN_FILE` for agenix and systemd credentials; no primary encryption key needed on peers.
- Added encryption/reopen/wrong-key, raw-file plaintext, rollback/concurrency/cursor, secret/OAuth persistence, reference validation and token-file checks. Updated deployment documentation with NixOS/agenix examples.

## OIDC deployment environment

- Removed the `oidc` JSON configuration block, config hot-reload handling and MCP settings preservation/redaction branches. Unknown config fields reject the removed block.
- Load dashboard sign-in once at startup using only `MCPPROXY_OIDC_ISSUER`, `MCPPROXY_OIDC_CLIENT_ID`, `MCPPROXY_OIDC_CLIENT_SECRET` and `MCPPROXY_OIDC_REDIRECT_URL`. No secret-store references or environment-placeholder expansion; the supplied client secret is literal.
- Validate partial/invalid environment settings before constructing the primary runtime. Entirely absent settings keep administration unavailable. Public OIDC clients may omit their client secret.
- Added environment-loading and removed-config rejection tests; updated deployment documentation.

## Remove token accounting, built-in TLS, Docker and audit logging

- Deleted local token counting and savings estimation, tokenizer vocabulary downloads/cache, per-call/session token metrics, and token-stat API fields. Session call counts and byte-based Usage statistics remain.
- Deleted the built-in TLS listener, certificate authority/key generation, TLS configuration/environment overrides and settings UI. The daemon serves HTTP; HTTPS belongs to the deployment reverse proxy. Outbound HTTPS/OAuth protocol support remains.
- Deleted the Dockerfile, Docker build/dependency-update entries, Docker path/environment discovery, container ownership/state/recovery/health/cleanup logic, instance-id persistence, launcher CID-file plumbing and Docker-specific diagnostics.
- Deleted the separate audit-log sink, configuration/environment overrides, settings UI, schemas and request instrumentation. Activity/history recording, sandbox refusal records and secret redaction remain independent of that removed sink.
- Removed feature-only code, obsolete documentation and dependencies. Removed config keys are rejected rather than migrated or aliased.


## Canonical server and profile storage

- Moved server and profile definitions into encrypted `primary.db`. JSON configuration and the global settings API contain only global settings; removed entity fields are rejected, with no import, migration or compatibility reader.
- Replaced duplicated server field mappings with one canonical server model, preserving icons, execution host, peer availability, headers and other current fields. Load servers, profiles and generation stamps from one consistent database snapshot at startup.
- Commit server/profile changes, OAuth clearing, profile reassignment and affected client bindings in one database transaction before publishing the runtime memory snapshot. Entity edits neither read nor write the settings file; settings reloads retain database entities.
- Removed server projection reconciliation, startup stamp repair, the unused asynchronous server-write queue, separate profile-pin compensation, and complete configuration snapshots from history identities.
- Added restart/field persistence, settings isolation, missing-settings-file, failed-publication and atomic rollback checks. No user instance or existing deployment data was changed.


## Remove output re-encoding, environment automation, header forwarding and tracing

- Removed TOON output encoding, per-server overrides, activity metadata, marker instructions, budget helpers and the TOON dependency.
- Removed global custom environment variables, automatic PATH discovery/enhancement, login-shell environment hydration/wrapping and ambient proxy-variable forwarding. Local processes use allowlisted inherited environment plus their explicitly configured server variables and arguments.
- Removed client-header forwarding settings and per-server allowlists, capture/filter/sink/redaction logic, HTTP method gates, peer-RPC header propagation and forwarded-header cache binding. Configured static server headers, OAuth authentication and ordinary credential redaction remain.
- Removed the unused repository-check setting and OpenTelemetry trace exporters, instrumentation, middleware and feature-only health manager. Health/readiness use their existing runtime controller; local Activity and Usage retain their cache/persistence settings. OpenTelemetry modules required transitively by Badger remain dependencies, without MCPProxy exporter/instrumentation code.
- Removed obsolete docs and config fields; no aliases, migrations or preservation logic. Added checks for rejected fields and retained explicit server environment behavior.


## Remove configurable policy bypasses and search diagnostics

- Removed search-debug config/flag/schema arguments, ranking explanations, debug response helpers and prose session-risk warnings (including the per-call opt-in). Structured session-risk data remains.
- Removed global read-only mode and its management-write gates. Safe environment inheritance, tool review, credential redaction in public views and strict intent validation now always use their former default behavior, without config fields or bypass branches. Per-server Trust all tools and individual tool disabling remain.
- Removed stored global review-policy metadata and plumbing from commits, storage, preflight and dispatch. Retained the per-server trust decision in the shared classifier.
- Default primary log files to `<data_dir>/logs` on Linux and macOS, including API and upstream logs; removed OS-specific log-directory selection. Explicit log-directory overrides remain.
- Removed obsolete Settings controls, updated current docs, added rejected-field/review-policy/log-directory checks, and preserved the uncommitted database and feature-removal work.


## Unify primary and peer startup configuration

- Replaced global Cobra option variables and inherited ignored flags with mode-owned command options. Both modes support data directory, listener and logging flags, paired with `MCPPROXY_DATA_DIR`, `MCPPROXY_LISTEN` and `MCPPROXY_LOG_*`; only primary has a config-file flag/environment variable.
- Removed Viper, the `MCPP_*` loader, the old `MCPPROXY_DATA` spelling, duplicate proxy overrides and the production-visible OAuth-disable testing switch. No aliases or compatibility paths remain.
- Explicit flags override environment/file values, including false and zero. Primary no longer forces file logging on or silently ignores a zero truncation override. Peer daemon/upstream logs use the configured peer log directory and verbosity.
- Made malformed deployment overrides fail startup, kept configuration discovery independent of data location, and stopped settings loads from creating overridden data directories. Storage creates the selected directory on startup.
- Updated startup/deployment documentation and added primary/peer precedence, credential-source and invalid-environment checks. No instance restart or commit.

## Immutable deployment settings — 2026-11-09

- Removed Settings navigation, route, form/JSON editor, command-palette entries,
  frontend API helpers and Monaco dependencies.
- Deleted settings REST handlers and management MCP tool, file watching/reload,
  restart-pending state, JSON commit/rollback and process-override persistence tracking.
- Global settings resolve at startup: flags → environment → JSON → defaults.
  Runtime entity commits reject global changes. Server/profile/client/peer/secret
  management remains available and writes to the database without touching JSON.
- Verified frontend type-check/production build, race-enabled backend tests,
  `go vet`, and macOS/Linux daemon builds. No instance restart or commit.

- Added environment-only `MCPPROXY_OIDC_CLIENT_SECRET_FILE` for agenix/systemd
  secret files. Reject ambiguous inline/file sources and unreadable/empty files;
  strip trailing line endings without trimming secret whitespace.

## Environment-only deployment configuration — 2026-11-09

- Replaced settings flags and JSON loading/discovery/bootstrap with one typed
  Config schema and caarlos0/env parsing. Both commands use the same loader.
- Secret fields (encryption key, OIDC client secret, peer token) support `_FILE`
  sources. All settings use typed parsing; secret inputs check source conflicts,
  bounded regular-file reads, line-ending removal, credential-file permissions
  and startup validation. Unknown names and wrong-mode settings fail startup.
- Centralized OIDC, encryption and peer credentials; consumers receive resolved
  settings instead of reading the environment again. No aliases or migrations.
- Removed configuration-path tracking from runtime snapshots, activity/history,
  server identities and script execution. Scripts use `<data-dir>/scripts`.
- Replaced obsolete config-file/flag tests and updated the settings inventory.
- Removed Cobra/pflag startup parsing and packaged JSON templates/config-file
  directory permissions. Only mode selection and help/version controls remain.
- Verified race-enabled backend tests, frontend type-check/production build,
  Go vet, vulnerability scan, macOS/Linux builds and help/version smoke checks.
  No daemon restart or commit.

- Renamed the peer primary address to `MCPPROXY_PEER_PRIMARY_URL` and the typed
  field to `PeerPrimaryURL`; no old-name alias.

## Remove upstream concurrency limiting — 2026-11-09

- Deleted global limits, per-server defaults and overrides, all six environment
  inputs, configuration validation/merging and API/MCP schema fields.
- Removed the limiter registry, pending queues, admission wiring, rejection
  observer and limiter-specific MCP/REST errors and Retry-After handling.
- Removed limiter-only rejected activity events, usage counters, response fields,
  frontend filters and documentation. Code-execution batch parallelism remains
  independent of this removed upstream limiting feature.
- Verified backend race tests, frontend type-check/production build, Go vet and
  macOS/Linux daemon builds. No daemon restart or commit.

## Dead-code and configuration-surface audit — 2026-11-09

Reviewed entity schemas, database read/write paths, HTTP/MCP management operations,
UI controls and environment settings. No production database was opened or
rewritten. No compatibility readers, migrations or aliases were introduced.

Removed:

- Unreachable configuration merging/diff machinery and duplicate helpers across
  authentication, contracts, diagnostics, HTTP handlers, OAuth, runtime, logging
  and upstream connection handling. Server snapshots now use the shared deep-copy
  implementation instead of a separate field-by-field copy.
- The unused tool-hash bucket/model/accessors. Current definition hashes and tool
  approval records remain; those drive actual review and change detection.
- Write-only approval timestamps (`definition_changed_at`, `pending_since`), the
  unused server shared-users flag, and unused profile-warning state.
- Client-record revocation/owner fields and client discovery, installation/path,
  reload, credential-check and expired-lease remnants. Current client tokens,
  deletion, rotation and presence remain.
- Request metadata written but never consumed, and the write-only filter
  diagnostic follow-up cache. Current request IDs and filter diagnostics remain.
- An uninitialized call-error recording pipeline, its transport recorder, and
  empty classification fields/filters. Actual call errors, upstream health and
  Retry-After handling remain.
- Sensitive-data detection fields, filters and events with no detector producing
  them. Credential redaction remains.
- Permanently disabled raw HTTP/SSE frame tracing and custom HTTP/JSON-RPC error
  types that were never constructed. Upstream protocol error handling remains.
- Unused frontend radio-group components and direct dependencies `clsx` and
  `tailwind-merge`; stale client/activity contract fields.

Corrected the logging directory environment name to `MCPPROXY_LOG_DIR` in the
canonical schema and settings inventory, with a loader regression assertion.
There is no `MCPPROXY_LOG_LOG_DIR` alias.

### Live behavior without normal UI/environment controls

These are findings, not deletions: their execution paths are active. A per-server
value cannot be configured through a global environment default unless explicitly
noted below. JSON tags on stored entity structs do not imply a supported JSON
configuration file or an editable HTTP request field.

| Feature/field | Current access | Gap |
| --- | --- | --- |
| `reconnect_on_use` | Server create/update through HTTP/MCP; used when calling a disconnected upstream | No UI toggle or environment setting. |
| `working_dir` | Server create/update through HTTP/MCP; used by stdio and local launchers | Displayed in server settings, but no create/edit input. |
| Per-server `init_timeout` | Server create/update through HTTP/MCP | No UI editor. `MCPPROXY_INIT_TIMEOUT` controls the global default. |
| Launch a local process and connect to its HTTP/SSE URL | API accepts both `command` and `url`, with a network protocol | UI supports a local stdio command or a remote URL, not this combination. |
| `launcher_wait_timeout` | Stored field consumed by the local HTTP/SSE launcher; defaults to 30s | No current UI, environment or management-request setter. |
| Per-server `health_check_interval` | Stored override consumed by health scheduling | No current UI or management-request setter. Global default is configurable with `MCPPROXY_HEALTH_CHECK_INTERVAL`. |
| Per-server `tool_discovery_interval` | Stored override consumed by discovery scheduling | No current UI or management-request setter. Global default is configurable with `MCPPROXY_TOOL_DISCOVERY_INTERVAL`. |
| Explicit `sse`, `streamable-http`, `auto` transport selection | Server API; upstream protocol support | UI uses automatic remote transport selection. |
| Manual upstream OAuth configuration | Stored OAuth fields consumed by OAuth setup: client ID/secret, scopes, extra parameters, callback/redirect and discovery endpoint overrides, PKCE control | UI only provides sign-in/logout; current server write request has no OAuth block and there is no environment input. This is a configuration gap for providers requiring manual client registration. Runtime OAuth discovery, tokens and callbacks are still needed. |
| Extra Activity query options | HTTP query supports `intent_type`, `include_call_tool`, `client_name`, `auth_type` | No ordinary Activity filter inputs; auth type can be represented in URL scope. These paths are live, not dead fields. |

Source anchors: [server fields](internal/config/config.go),
[server write request and patching](internal/httpapi/server.go),
[server creation](internal/httpapi/management_servers.go),
[upstream scheduling and reconnect-on-use](internal/upstream/manager.go),
[local HTTP/SSE launcher](internal/upstream/core/connection_launcher.go),
[Activity queries](internal/httpapi/activity.go).

Profile-switch destinations have actual UI checkboxes and were retained. MCP-only
script execution controls and calculated/protocol-owned fields were not classified
as missing UI settings. Upstream concurrency limits and per-server queues remain
removed.

Validation: all-package backend tests with the race detector, Go vet, frontend
TypeScript/production build, Go reachability analysis including package tests,
and frontend unused-file/dependency analysis. Static analysis is not a proof that
every reachable branch is useful. No daemon restart or commit.


## Remove per-server timing overrides — 2026-11-09

- Removed per-server initialization timeout, health-check interval and discovery
  interval fields from stored entities, HTTP/MCP server requests and responses,
  update logic, connection comparisons and validation.
- Retained the global environment settings `MCPPROXY_INIT_TIMEOUT`,
  `MCPPROXY_HEALTH_CHECK_INTERVAL` and `MCPPROXY_TOOL_DISCOVERY_INTERVAL`.
- Removed the minimum-of-per-server-interval scheduler and unused config
  snapshots; periodic discovery now uses the single global cadence.
- Verified all-package backend race tests and Go vet. No migration,
  compatibility reader, daemon restart or commit.

## One connection controller — 2026-11-09

- The primary's connection controller is the sole scheduler for upstream
  connection attempts and retries. Health monitoring only probes and reports
  failures; it no longer reconnects. Removed the independent 60-second runtime
  connection sweep and `ConnectAll` plumbing.
- OAuth completion, refreshed credential availability and peer readiness changes
  notify the controller through coalesced wake requests instead of dialing from
  callbacks. Server edits and Restart install the current committed instance and
  wake that same controller. Peers execute the controller's connection requests.
- Removed `reconnect_on_use` from stored server entities, HTTP/MCP requests,
  responses, update logic and dispatch. Tool calls do not initiate reconnection
  or automatically replay a call.
- Removed forced/synchronous reconnect helpers, their separate locking and wait
  loop, duplicate retry-policy methods, obsolete teardown helpers and unused
  unbound server management methods.
- Retained exponential backoff, OAuth sign-in waits and longer retry delays,
  permanent-error parking, infrequent recovery probes, explicit logout/disabled
  checks, and committed-generation/instance protections. Active connections and
  connection attempts are excluded from retry eligibility.
- New credential/peer-state notifications can reopen a parked attempt while
  preserving upstream Retry-After deadlines and the failure information needed
  to protect ongoing calls from premature teardown.
- Regression checks cover controller wake delivery/coalescing, retry eligibility,
  notification-only credential recovery, stale credential bindings, disabled/
  logged-out/retired instances, Retry-After, health checks that never dial and
  protection of in-flight calls. Verified backend race tests, Go vet, static
  reachability checks and macOS/Linux builds. No daemon restart or commit.


## Remove extra Activity filters — 2026-11-09

- Removed `intent_type`, `client_name` and `auth_type` Activity query parsing,
  storage filter fields/predicates and unused intent-filter extraction.
- Removed `include_call_tool` query control. Activity continues to hide duplicate
  internal dispatch records automatically; internal usage aggregation can still
  read its complete input population.
- Removed the frontend auth-type URL filter/client-side filtering and unused
  API parameter declaration. Stored client identity, auth attribution and intent
  metadata remain available on activity records.
- Verified backend tests and frontend TypeScript/production build. No daemon
  restart or commit.


## Editable upstream OAuth defaults and overrides — 2026-11-09

- Added a compact OAuth section within the Connection card. Automatically
  resolved fields are prefilled; edited fields become overrides. Each field has
  a reset-to-automatic control, with one Save/Cancel action for the changes.
  Extra parameters use name/value inputs and client secrets support temporary
  reveal and secret references.
- Added validated per-field OAuth patching to the shared server HTTP/MCP write
  path. Omitted fields survive, null removes an override, and removing the last
  override restores automatic OAuth detection. Existing server storage remains
  the only source of saved overrides; discovered values are never copied into it.
- Added a read-only administrator settings endpoint and matching management MCP
  operation. Discovery uses the execution host and the shared OAuth cache, runs
  even with overrides set, respects cancellation/deadlines, and neither connects
  a server nor registers clients/starts callback listeners. Peer reads do not
  configure running servers or change OAuth credential leases.
- Shared automatic scope/endpoint resolution with actual OAuth configuration;
  retained refresh-token scope discovery and endpoint rewriting. A configured
  metadata URL supplies other automatic fields, while its own reset value comes
  from ordinary upstream discovery.
- Removed duplicate scope discovery and the inert configurable PKCE boolean;
  actual OAuth already always used PKCE and continues to do so.
- Verified reset/preservation/validation, discovery independent of overrides,
  effective OAuth endpoint selection, read authorization/cache headers, peer
  discovery without execution changes, and nullable management MCP schemas.
  Backend race tests, Go vet, frontend TypeScript/production build, static
  reachability checks and macOS/Linux builds pass. No daemon restart or commit.

## Primary OIDC startup checks and demo credential repair — 2026-11-09

- Primary mode now requires OIDC issuer, client ID and callback settings.
  Discovery and usable public signing keys are checked before opening runtime
  storage or the HTTP listener. The validated provider is reused for sign-in.
- Confidential client credentials are checked non-interactively when the provider
  advertises authenticated token introspection, using an intentionally invalid
  token. Rejected credentials stop startup. Providers that do not offer this
  check still verify credentials during sign-in; public clients remain supported.
  Introspection permission is separate from sign-in permission.
- OIDC credential POSTs cannot redirect to another origin. Startup errors omit
  client secrets and provider response bodies.
- Corrected the explicitly authorized one-off demo import: Keychain's encoded
  representation had been copied instead of decoded values. Repaired the demo
  OIDC secret file and 11 imported encrypted secrets, then restarted the demo.
  Pocket ID issuer/client/callback settings and Pocket ID itself were unchanged.
  No Keychain decoding or migration support was added to MCPProxy.
- Verified full backend race tests, Go vet, startup rejection tests for missing
  settings/bad clients/secrets/signing keys, and that failure occurs before
  storage creation. The repaired demo passed Pocket ID credential validation and
  became ready; its sign-in URL retains the original client and callback.

## Transport-correct initialization timeout diagnostics — 2026-11-09

- Fixed shared MCP initialization code that described every timeout as a local
  subprocess/stderr failure, causing remote servers to receive a stdio error.
- Remote initialization timeouts now carry the existing HTTP timeout code and a
  transport-neutral cause. Stdio alone includes captured subprocess stderr.
  Both retain the original deadline error for retry/authentication decisions.
- Text-based stdio handshake classification now requires a stdio transport hint.
  Expired requests do not start another auth strategy or report a misleading
  zero-second initialization attempt.
- Verified race-enabled diagnostics, upstream, runtime, server and peer tests.
