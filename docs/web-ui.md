# Web console

The console starts at `/admin` and opens Overview. Usage, models, API keys,
audio share one navigation and one admin session.
`/usage` redirects to `/admin/usage`; `/usage/data` redirects to the protected
`/admin/usage/data` endpoint, preserving its query string. The separate usage
login and logout endpoints have been removed. Old usage sessions must sign in
again using the admin password.

```yaml
admin_password: "replace-with-a-strong-password"
log_metrics: true
usage_db: usage.db
```

The existing `usage_dashboard: true` / `usage_dashboard_password` configuration
continues to provide a fallback admin password. A separate `usage_dashboard`
switch is not required when `admin_password` is set. With metrics disabled,
Overview explains how to enable logging; all other console pages remain usable.
The optional client setup generator at `/` still uses `serve_config_generator`.
API keys are managed locally in `keys:` or on the API keys tab. Self-service
enrollment, invitation links, email confirmation and SMTP have been removed.

## Browser API access

Add exact browser origins to the local `config.yaml`:

```yaml
cors_allowed_origins:
  - http://pdf.ts.localhost
  - http://epub.ts.localhost
```

Origins include the scheme and, for non-default ports, the port. A trailing `/`
is accepted; paths, credentials and wildcards are rejected. An omitted/empty
list disables cross-origin access. Changes apply on config hot reload.

CORS applies to `/v1/*` and `/anthropic/*`. Allowed preflight requests return 204 before authentication;
actual requests still use the existing API-key checks. Cross-origin cookies are
not enabled and `/admin/*` remains same-origin. Supported methods are GET/POST;
allowed headers cover Authorization, Content-Type, x-api-key and the standard
Anthropic/OpenAI version/organization/project headers. Use a dedicated proxy key
for browser clients, keeping upstream provider credentials in the server config.

## Frontend files

- `internal/handler/web/templates/`: HTML pages and shared shell/login templates.
- `internal/handler/web/static/`: local CSS and JavaScript, served at `/assets/`.
- `internal/handler/web.go`: embedding, template rendering and static asset serving.

Edit the frontend files directly and rebuild with `go build .`. No Node build,
CDN, font download or separate frontend server is required. `go:embed` packages
the frontend in the binary, so edits take effect after rebuilding and restarting.
Only static assets are publicly served; templates and directory listings are not.
Page HTML uses Go's template escaping. Trusted HTML slots contain only embedded
page fragments, never request input. Existing inline event handlers remain, so
the console CSP still allows inline scripts and styles.

Run `go test ./internal/handler` for console, authentication and API coverage.
When Node is installed, this includes syntax checks for every JavaScript asset.

## Trimming

This change removes the second usage login/session flow, the duplicate usage
page shell, the old brand mark and external font requests. The duplicate
Processors editor is removed; `/admin/processors` redirects to Models → Helpers,
and its data/mutation APIs remain available. Existing proxy API capabilities are retained.

Further candidates, in order of likely maintenance benefit:

| Area | Benefit | Tradeoff |
| --- | --- | --- |
| Legacy processors API | Consolidate with the Models helpers API | The current Models UI still uses it; migrate those calls first |
| Client config generator and public landing assets | Reduce the largest frontend script and documentation surface | Loses assisted setup for coding clients |
| Connect hub integration | Remove remote discovery UI and transport integration | Requires confirming that no `connect://` backends are used |
| Audio | Removes speech recognition and synthesis routing | Keep if any client uses audio endpoints |

Hiding navigation alone does not reduce the binary or backend maintenance.
Removing a subsystem should include its routes, configuration, implementation,
tests and documentation, after verifying it has no consumers. Shared auth,
rate limiting, streaming, health checks and usage retention are not candidates
for cosmetic trimming.

## Removed services

Proxy-executed web search (Tavily/Brave), `/v1/search`, the search MCP endpoints,
the Qdrant proxy, PDF parsing/OCR/rendering and Enrollment/SMTP are removed.
The agent/application performs retrieval, search and document preparation.
Ordinary function-tool definitions, calls and results are still forwarded,
including a client-owned function named `web_search`. The configuration generator
does not configure search services or disable the agent's own web search.

Native Responses file URLs and existing upstream file IDs can pass through.
When translating to Chat Completions, inline file data and file IDs are retained;
document URLs produce an explicit error instead of being fetched by the proxy.
The proxy does not provide a Files upload/storage API.

## Daily usage storage

The overview reads daily UTC aggregates rather than request-level rows. Existing
databases migrate in place at startup. User/model/backend views and weighted
latency remain available. Writes batch for up to ten seconds; overview/activity
queries and normal shutdown flush pending counts. See [Usage statistics](usage.md).
