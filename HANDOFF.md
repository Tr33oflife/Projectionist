# HANDOFF — Projectionist (Plex Collection Manager)

This document explains what the app is, how it is built, and the
rules any future work must keep. It ships with every release. The
per-release run history is kept in the project's private archive,
not in shipped documents.

## What it is

Projectionist is a single-binary desktop app with a real GUI for
managing a Plex library's presentation layer:

- **Poster overlays** — data-driven badge families (resolution,
  HDR, audio, ratings, streaming service, franchise, "leaving
  soon", and more) judged per item against its real Plex
  metadata, in three visual styles, for movies and shows at the
  top level, at season level, and at episode level.
- **Collections** — a manager (health view, full metadata
  editing with verified saves, bulk artwork) and a creator (a
  guided builder with dry-run diffs, smart rule-based
  collections, an advisor that mines the library for worthwhile
  collections, and TMDb/Trakt list import with honest
  unmatched-item reporting).
- **Poster restore** — one flow returns items to their clean
  Plex agent posters, undoing baked-in overlays from other
  tools.

It is a Kometa alternative in spirit: the same data-driven
overlay model, with a GUI instead of YAML, a dry-run before
every write, and a verified backup behind every poster change.

## Architecture

- Go, one module, standard library plus `golang.org/x/sys`
  (Windows DPAPI for secret storage). No other dependencies.
- `main.go` — local HTTP server on 127.0.0.1 only (port 38234,
  free-port fallback), a per-launch session token, and the
  embed of `web/`, `assets/badges/`, and
  `assets/collection-art.json`. The version lives in one place:
  the `version` const in `main.go`.
- `web/` — the embedded UI (plain JS, no framework, no build
  step). The badge compositor runs client-side on Canvas2D;
  the same evaluation logic is mirrored server-side for
  apply-time judging, and a shared test suite keeps the two in
  agreement.
- `internal/` — `plex` (client, connection ranking and
  failover, writes behind a safety policy), `api` (HTTP
  handlers, overlay apply engine, restore engine), `overlays`
  (families, templates, sanitizers), `advisor` (suggestion
  generators), `meta` (TMDb, Trakt, OMDb, and an
  OpenAI-compatible AI client), `collectionart` (collection
  artwork lookup), `store` (per-user settings, secrets,
  templates, history, backups).

## Standing boundaries (do not cross)

1. **Media deletion is disabled by design.** The only deletion
   the app can perform is collection membership edges. There
   is no collection deletion either.
2. **Poster writes are backup-gated.** Before any poster is
   written, the current poster is downloaded, verified as a
   real image, and stored write-once with a checksum sidecar.
   A failed gate means no write.
3. **Writes are sent exactly once.** Uploads walk the server's
   ranked connection list, but a request that may have been
   sent is never replayed on another connection; an unknown
   outcome is verified by reading back, and reported honestly
   either way.
4. **No facts are invented.** A badge fires only on metadata
   the server (or a configured metadata provider) actually
   reports. Missing data means no badge, with the reason named
   in the UI. HDR10+ is never reported or inferred.
5. **Keys belong to the user.** Every external key (Plex,
   TMDb, Trakt, OMDb, AI) is supplied by the user in Settings
   and stored only in the user's own data directory
   (`%APPDATA%\Plex Collection Manager` on Windows). Nothing
   is embedded, phoned home, or shared.
6. **Trakt sign-in is out-of-band only** (the OOB redirect
   URI); there is no embedded redirect endpoint.

## Verification philosophy

Every release is verified the same way before it ships:

- **Unit and fake-server suites** (`go test ./...`) pin the
  Plex client's request shapes, the safety policy, template
  sanitizers, the overlay evaluators, and the API contracts
  against a scripted fake Plex server.
- **A DOM harness** boots the real shipped page in a simulated
  browser and clicks the real controls end to end.
- **A live battery** runs against a throwaway, unclaimed test
  Plex server with dummy media: connect, evaluate, apply,
  verify the served bytes changed, restore, verify again.
- **Poster changes are proven at the byte level** where the
  claim is about what clients display, not just what the API
  answered.

Live verification against anyone's real library is always the
library owner's own run; development testing never touches a
real server.

## Release shape

A release is one ZIP containing the Windows executable, the
Linux binary, `README.md`, this `HANDOFF.md`, the `assets/`
tree, and the project's public plan documents. Version is
bumped only in `main.go`; the running app reports it through
`GET /api/health`, and the UI reads its version from there.

## License

MIT — see `LICENSE`.
