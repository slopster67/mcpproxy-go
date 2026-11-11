# MCPProxy frontend

Vue 3, TypeScript, Pinia, Vue Router, Tailwind CSS 4, and local shadcn-vue
components. The interface uses the current shadcn Luma style, Geist Variable,
and the neutral palette in dark mode.

## Development

Use Node.js 26.13 or newer.

```sh
npm ci
npm run dev
```

Open `http://127.0.0.1:3000/ui/`. Vite proxies `/api` (including the
`/api/v1/events` change stream) to `http://localhost:8085`; run a backend on that port. Sign-in uses a browser session
cookie from the OIDC provider, so use the same hostname when reopening the page.

```sh
npm run type-check
npm run build
```

The production build goes to `dist/` for embedding by the Go backend.

## Structure

- `src/components/ui/`: local shadcn-vue Luma components backed by Reka UI.
- `src/assets/main.css`: semantic design tokens and Tailwind.
- `src/lib/utils.ts`: the shared `cn` class-merging helper.
- `components.json`: shadcn-vue component configuration.
- `src/components/`: application controls and dialogs.
- `src/views/`: Servers, Clients, Profiles, Tools, Secrets,
  Activity, Usage, Settings, and their detail/editor views.
- `src/router/`: routes and redirects; Servers is the default page.
- `src/stores/`: one live collection store per entity (`collection.ts`), loaded
  on first use and kept current from the change stream.
- `src/services/api.ts`: the REST client, one method per route; failures throw `ApiError`.
- `src/services/events.ts`: fan-out of `/api/v1/events` frames to stores and pages.

## Components

Use the local Button, Badge, Card, Alert, Input, NativeSelect, Textarea, Switch,
Checkbox, Table, and Skeleton components. Sidebar, menus, dialogs, toggles and
selection controls use shadcn-vue's Reka UI primitives for focus management and
keyboard interaction. Input, Textarea and NativeSelect retain adapters for the
application's existing native events, values and numeric/selection semantics;
their presentation follows the registry.

Read the official [shadcn-vue agent skill](https://github.com/unovue/shadcn-vue/blob/dev/skills/shadcn-vue/SKILL.md)
before adding or replacing primitives. `components.json` selects `reka-luma`.
Use the CLI and inspect its diff; avoid recreating component appearance in callers.
The [official customizer](https://ui.shadcn.com/create) previews alternative styles.

Use semantic tokens (`background`, `card`, `foreground`, `muted-foreground`,
`border`, `primary`, `destructive`, `success`, `warning`, `info`) rather than
adding page-specific palettes. Keep interaction and API logic in the existing
application components and stores.

## UI conventions

- Use the local Tabs, TabsList, TabsTrigger and TabsContent for page views and
  status tabs. They use Reka focus management and automatic keyboard activation.
  Let the registry variants define their appearance.
- Keep page headings compact and align contextual actions with the heading.
  Use one primary action per section; refresh, edit and overflow actions are
  secondary. Use NativeSelect for secondary filters rather than painted buttons.
- Prefer bordered surfaces and divided rows to nested cards, oversized counters,
  or decorative shadows. Reserve status colors for information that needs them.
- Let Button variants own their appearance. Do not repeat their colors and
  borders in each caller. Use semantic tokens for text and surfaces.
- Preserve URL filters, native input events, labels, focus handling, and unsaved
  forms when changing presentation. Settings panels remain mounted across tabs.

Server-card status dots use the `--server-status-*` colors: vivid Display P3
with sRGB fallbacks. Keep these dots flat, without a glow or shadow.

## Unified tool inventory

Tools is the single browsing and review page, grouped by server. Search, server
and Needs review filters share the same rows; availability, tier and view-as
controls are inline. Local filters never refetch the inventory. Row actions update
in place, and live data reconciles in the background without global loading
states or clearing unrelated details. Tool order stays alphabetical. Server pages contain configuration/logs and link to the filtered
inventory. Old review and server-tools URLs redirect to Tools.

ToolSection renders sanitized Markdown descriptions in pure-black blocks,
syntax-highlighted input/output schemas, annotations, previous definitions/diffs,
and hold evidence. Usage, last used and the calls link share one row above the
description; profile access information remains available. Raw approval-record
and metadata views are removed. Approvals use
the captured review fingerprint and server incarnation; view-as is read-only.
The catalogue retains saved tools while a server is unavailable. Do not replace
the review snapshot with a fresher definition without refreshing its binding.


The shell has no top bar. CommandPalette mounts globally and opens with
Ctrl/Cmd+K or `/` outside editable fields. The shell and page chrome render
immediately: route views are included in the initial UI bundle, while charts
and the JSON editor load on demand. Only API-driven content gets skeletons;
headings and toolbars stay mounted while data loads. The sidebar resolves its
initial highlight from the URL. A mobile-only drawer opener remains.

Loading regions generally delay skeleton visibility by 150 ms, without delaying
completed content; Secrets shows its skeleton immediately. Server skeletons share the real card components and row geometry;
review-count badge appearances fade without changing layout.

The palette uses Luma CommandDialog with the shared blurred Dialog overlay.
Command primitives manage highlighting, selection, focus and dismissal. The
CommandInput `filter` adapter can disable its local substring filter when the
application supplies already-filtered results, including semantic tool search.

Server detail defaults to Configuration, followed by Logs and Tools. Both Tools
locations render ToolInventory; server detail fixes its server scope and omits
the redundant server card heading and its divider, retaining the card container. Bulk approval appears in the toolbar immediately before Refresh. Both inventory and review requests accept a server
filter. Logs load only when selected, with delayed skeletons for initial loading.

Page/content loaders use skeletons. Secrets shows them immediately; Clients,
Activity, Usage and embedded tool/activity lists use the shared 150 ms
policy. Action-button spinners and rotating refresh icons remain appropriate
feedback; existing content should stay visible while refreshing.

Add server is a BaseDialog over Servers (`/servers?add=1`) reusing ManualServerForm.
The form emits success/cancel/busy state; the parent owns navigation and closing.
Successful additions keep users on the server list.

Add/edit process forms share ArgumentFields; arguments are individual values,
never parsed by whitespace splitting. The read-only process display still joins
arguments on one line.

Clients is a single page. Endpoint URLs live at its bottom; routing/schema
settings live only in Settings. It uses the shared 24px card spacing and native
Luma dialogs, profile menus, fields and actions.

Create a client with a name and optional profile, then copy its generated token
into the AI client. Named profiles expose an Allow switching toggle, off by
default. All servers always permits switching and hides the toggle in creation
and editing. Client tokens do not expire. Only saved client tokens and OAuth sign-ins
issued for a saved client authenticate MCP connections; anonymous connections, the dashboard admin key and standalone
agent tokens are rejected. The admin key still authenticates the dashboard REST API.

Delete removes the saved client and its token, refreshing the roster in place.
Rotation immediately invalidates the current token and returns its replacement;
there is no pending/finalize step. Client status dots use active sessions
(green connected, white disconnected). Activity history is retained. One-time tokens stay inside
the dialog component and are never persisted by the frontend.

Clients and Profiles use compact native Luma card grids. Client actions live in
the popup opened by clicking a card; Profiles retains three-dot menus.
Client tokens use copyable fields and formatted, highlighted JSON on black.
Profile editing opens Permissions, followed by Configuration and Clients,
preserving saved access and unsaved drafts. Activity uses a compact event list and detail
dialogs; Usage groups its charts into an overview and a tabbed Tools card.

Profile permissions use one grouped checklist. Selecting a server grants all its
current and future approved tools; selecting individual tools stores exact names
in `selected_tools`, excluding future tools. Quick select fills that same checklist
with read-only tools, read/write tools, all listed servers, or nothing. Approval,
global tool enablement and server availability still apply.

Only the current permission format is supported. Removed tier caps, pattern rules,
old routes and backend feature negotiation are not retained or migrated. Unknown
configuration fields fail validation. See AGENTS.md for the development rule.

Unannotated and manually classified tools have an unfilled, dashed tier badge.
Click it to choose Read, Write or Destructive, or reset it to Unannotated.
Write is yellow and Destructive is red; server-provided annotations stay read-only.
Classification is definition-bound and does not approve or enable a tool.
View calls uses an SVG icon, so the arrow renders consistently on iOS.

Profile capability toggles reflect global settings: disabled globally means off
and locked, with a short explanation only for that toggle. When globally enabled,
the profile may still disable the capability. Read-only mode also locks server
management off. Global locks do not overwrite the profile's saved preferences.
