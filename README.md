# Projectionist — Plex Collection Manager

**Projectionist** dresses up your Plex library and keeps it dressed:
data-driven **poster overlays** (badges for resolution, HDR, ratings,
streaming service, and more — earned by each title's real metadata, never
guessed), a **collection manager** that keeps your collections healthy, and
a **collection creator** with an advisor that finds the collections your
library is missing. If you have used Kometa, this is that idea with a GUI,
a dry-run before every change, and a verified backup behind every poster it
touches.

One self-contained binary. No installer, no accounts, nothing leaves your
machine except the requests you configure (Plex itself, plus whichever
optional services you add keys for).

## Why this exists

> This was made to solve a problem I had — trouble with Kometa, especially
> on my seedbox. If it can help you as well, cool — I appreciate all
> feedback.
>
> **I will never charge for this product.** I do have a donate link
> included, with no expectation of anyone using it.
>
> Hopefully this app gives the basic overlay functionality to laymen users
> that Kometa seems to overlook. Plug in your Plex, and get editing.
> Jellyfin maybe added in the future. Big maybe.

## What it does

**Poster overlays.** 34 badge families in three styles (classic, aurora,
and a procedural frosted-glass "signature" style): resolution, HDR
(Dolby Vision / HDR10 / HLG), audio channels, video codec, subtitles and
languages, runtime, watch status, new releases, ratings, content rating,
franchise and saga position, origin, genre banners, collection membership,
"Leaving Soon" dates you set, awards, Rotten Tomatoes Fresh, bitrate,
in-progress, decade, studio, and more. Plus **custom badges**: pin any
free text to a title or collection, or define rule-based badges (genre,
studio, tag, rating, year, watch status) that fire like built-ins. Every
badge is opt-in — nothing new is forced into an existing template. Badges fire only when the underlying
fact exists in Plex metadata — a missing fact means no badge, never a
guessed one. Overlays apply to movies, shows, seasons, and episodes
(episodes get a 4-badge cap on a landscape canvas). Every apply writes a
verified backup first, so any poster can be restored exactly.

**Collections manager.** Browse, edit, duplicate, and delete collections
from a poster grid of your actual library. Edit any collection's details,
members, sort title, summary, and content rating; bulk-select across the
library; check collection health (missing art, duplicates, member counts).

**Builder wizard.** A 4-step guided flow — Start (blank, from rules, or
from a list), Library & members (a poster grid of the library's real
titles, with search and drag-reorder), Details, Review & create. Nothing is
created until Review: the dry-run is computed on entry, and abandoning the
wizard writes nothing. Both static collections (a fixed member list) and
smart collections (Plex rules that stay current as your library changes)
are supported, including content rating on create.

**Advisor.** Scans a library section and suggests collections it is
missing: franchises, genres, decades, ratings standouts, hidden gems,
dub/sub groups, director and actor spotlights, studio shelves, decade ×
genre pairs, unwatched gems, rewatchables, quick picks, epics, and
critic/audience divergence shelves — all mined from the library itself,
and suppressed when they overlap an existing collection. And more. Each suggestion opens in a full editor (title,
reason, rules summary, member wall) before anything is created. Two AI
features, both optional and both using your own configured provider:
natural-language collection rules ("movies like…") and **AI collection
suggestions** — the model proposes whole collections from your library,
and every suggested member is verified against your real library before
the card exists (the card shows how many of the model's titles verified).

**Poster generator.** Build a collection poster from its members: mosaic
grids (2×2, 3×3), an "All — Flower" layout (center poster with radiating
petal rings), and an "All — Spiral" layout — plus Hero and Title card
designs with member art, your own upload, a URL, AI-generated backgrounds,
or derived colors, with blur/darken/gradient and focal controls. Mosaic
layouts have a 50–200% poster zoom. Preview writes nothing to Plex; Apply
rides the same verified backup gate as overlays.

**Restore.** "Restore default posters" returns top-level items to their
original posters, and "Restore backed-up posters" covers movies, shows,
seasons, and episodes with a level filter — anything that was ever backed
up can come back exactly. Items with no backup are never listed, and no
provider poster is ever passed off as an "original".

**Themes.** The whole UI runs on CSS variables with six built-ins
(Projectionist, Midnight, Cinema, Light, High Contrast, and a Plex homage
in charcoal and amber), plus a custom theme editor with export/import.

**Settings backup.** Export your settings — themes, templates, Leaving
Soon dates, and portable preferences — to a JSON file, and import it on
another machine. API keys are excluded by default (an explicit opt-in
includes them); server tokens and session state never export.

## Screenshots

Shot against a demo library of public-domain classics (posters from
archive.org — Night of the Living Dead and friends), so what you see is
the real app, not a mockup:

| Collections | Studio inspector |
| --- | --- |
| ![Collections](docs/screenshots/01-collections-grid.png) | ![Studio](docs/screenshots/02-studio-metropolis.png) |

| Advisor | Builder |
| --- | --- |
| ![Advisor](docs/screenshots/03-advisor.png) | ![Builder](docs/screenshots/04-builder-grid.png) |

| Generator — Flower | Restore backed-up posters |
| --- | --- |
| ![Generator Flower](docs/screenshots/05-generator-flower.png) | ![Restore](docs/screenshots/06-restore-card.png) |

### Themes

| Projectionist (default) | Light |
| --- | --- |
| ![Default theme](docs/screenshots/theme-proof-default.png) | ![Light theme](docs/screenshots/theme-proof-light.png) |

| Cinema | Plex homage |
| --- | --- |
| ![Cinema theme](docs/screenshots/theme-proof-cinema.png) | ![Plex theme](docs/screenshots/theme-proof-plex.png) |

### Poster generator

| Mosaic | Flower | Spiral |
| --- | --- | --- |
| ![Mosaic](docs/screenshots/generator-proof-mosaic.png) | ![Flower](docs/screenshots/generator-proof-flower.png) | ![Spiral](docs/screenshots/generator-proof-spiral.png) |

| Hero | Title card | AI background |
| --- | --- | --- |
| ![Hero](docs/screenshots/generator-proof-hero.png) | ![Title card](docs/screenshots/generator-proof-titlecard.png) | ![AI background](docs/screenshots/generator-proof-ai.png) |

### Overlays and collections

| Badge library | Leaving Soon | Episode overlays |
| --- | --- | --- |
| ![Badge library](docs/screenshots/badge-library-proof.png) | ![Leaving Soon](docs/screenshots/leaving-proof-soon.png) | ![Episode overlays](docs/screenshots/episode-pass-after.png) |

| Signature style | Layman's pack |
| --- | --- |
| ![Signature style](docs/screenshots/signature-real-render.png) | ![Layman's pack](docs/screenshots/layman-pack-proof.png) |

## Install and run

### From a release (recommended)

1. Download the latest release ZIP for your platform and extract it
   anywhere.
2. Run the binary:
   - Windows: double-click **plex-collection-manager.exe**
   - Linux: `./plex-collection-manager`
3. A console window opens and prints a local address
   (http://127.0.0.1:38234/). Your browser opens to it automatically; if
   it doesn't, paste the address in yourself. The interface is served only
   on your own machine.
4. To stop the app, close the console window (or Ctrl+C).

Your data lives in `%APPDATA%\Plex Collection Manager` on Windows
(`~/.local/share/plex-collection-manager` on Linux): settings, templates,
backups, and history. A fresh install starts empty.

### First run

1. Click **Sign in with Plex**. A plex.tv tab opens; approve the app there.
   No password is ever typed into Projectionist — Plex issues a limited
   token for this app.
2. Pick the server you want to manage. Your libraries and collections
   load, and you can browse, build, and design right away — overlays and
   collections work with no other setup.
3. Optionally, add the keys below in **Settings** to unlock the features
   that use outside data.

### Build from source

Requires Go 1.27 or newer.

```
git clone https://github.com/Tr33oflife/Projectionist.git
cd Projectionist
go build -o plex-collection-manager .
./plex-collection-manager
```

For a Windows binary from any platform, the checked-in
`resource_windows_amd64.syso` (the app icon resource) is picked up
automatically by the Go toolchain:

```
GOOS=windows GOARCH=amd64 go build -o plex-collection-manager.exe .
```

Tests: `go test ./...` (Go unit tests) and
`node web/overlays.dom.test.cjs` (the browser-UI harness, jsdom).

## Optional keys (yours, entered in Settings)

Projectionist embeds no keys of its own. Each of these is **your own key**,
stored only on your machine:

| Key | Where to get it | What it unlocks |
| --- | --- | --- |
| **TMDb API key** | themoviedb.org → Settings → API | Collection artwork, franchise/saga badges, origin data |
| **Trakt client ID + secret** | trakt.tv → Settings → Your API Apps (create your own app; each user registers their own) | Trakt list import, watchlist-based suggestions |
| **OMDb API key** | omdbapi.com | IMDb ratings (Plex has no IMDb feed; ratings shown come from OMDb and are labeled as such) |
| **Fanart.tv API key** | fanart.tv | Transparent logos for the overlay studio |
| **AI endpoint + key** | any OpenAI-compatible provider | Natural-language collection rules and AI collection suggestions in the Advisor; AI poster backgrounds in the generator |

A note on ThePosterDB artwork: art comes from ThePosterDB. If art requests
suddenly fail as blocked, their anti-bot protection has tripped on your
connection — it usually clears on its own; wait a while and try again.

## Safety notes

- **Media is never deleted.** Only collection-membership edges are
  removed; collections themselves are never deleted by this app, and media
  files are never touched.
- **Every poster write is backed up.** Before any poster is replaced, the
  current poster is downloaded, verified as a real image, and stored with
  a sidecar. Restore returns the exact backed-up bytes.
- **No HDR10+ reporting.** The app reports only what Plex metadata and
  stream facts actually establish; HDR10+ is never claimed.
- **IMDb Top 250 is deliberately excluded** from ribbon honors.

## Support the project

If Projectionist is useful to you and you'd like to toss a few bucks its
way, there's a small Ko-fi button in the app (Settings → Donate button;
it appears when you're online and stays out of the way otherwise), or
directly at [ko-fi.com/t4v3rn](https://ko-fi.com/t4v3rn). No obligation,
no nagging — the app never asks twice.

## License

MIT — see [LICENSE](LICENSE).
