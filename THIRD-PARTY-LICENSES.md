# Third-party licences

Every dependency of `compute-api/` and every component `deploy/` runs is listed
here, in the commit that adds it. Permissive licences only (ADR-0002).

## compute-api (linked into our binary)

| Component | Licence | Notes |
|---|---|---|
| Go standard library | BSD-3-Clause | |
| [github.com/jackc/pgx/v5](https://github.com/jackc/pgx) v5.8.0 | MIT | Postgres driver and pool for the job store (`internal/store`). Chosen over `lib/pq` for native `jsonb`/`bytea`/array handling and a maintained pool; v5.8.0 is the newest release whose `go.mod` still accepts Go 1.24, which the Dockerfile builds with. |
| [github.com/jackc/pgpassfile](https://github.com/jackc/pgpassfile) v1.0.0 | MIT | Transitive, via pgx: `.pgpass` lookup. |
| [github.com/jackc/pgservicefile](https://github.com/jackc/pgservicefile) v0.0.0-20240606120523-5a60cdf6a761 | MIT | Transitive, via pgx: `pg_service.conf` lookup. |
| [github.com/jackc/puddle/v2](https://github.com/jackc/puddle) v2.2.2 | MIT | Transitive, via `pgxpool`: the connection pool. |
| [golang.org/x/sync](https://pkg.go.dev/golang.org/x/sync) v0.17.0 | BSD-3-Clause | Transitive, via puddle (`semaphore`). |
| [golang.org/x/text](https://pkg.go.dev/golang.org/x/text) v0.29.0 | BSD-3-Clause | Transitive, via pgx: SASLprep for SCRAM passwords. |

The modules above are what `go list -deps ./...` reaches; `go.mod` lists
nothing else. pgx's own test dependencies (testify and friends) are not in
our build graph. The `cmd/compute-api` binary links them once it imports
`internal/store`, which it does not yet.

## Run alongside, over HTTP or as a separate container (not linked)

| Component | Licence | Relationship |
|---|---|---|
| [SkyPilot](https://github.com/skypilot-org/skypilot) via `FelixSphere/skypilot` | Apache-2.0 | Orchestration backend, driven through its API server. Carries a NOTICE obligation for whichever packaging ships it. |
| [new-api](https://github.com/QuantumNous/new-api) via `FelixSphere/unifyapi-console` | AGPL-3.0 | Account plane. HTTP only. Nothing from it is copied into this repository; see ADR-0002. The fork's own source stays public. |
| PostgreSQL | PostgreSQL Licence | Container in `deploy/compose`. |
| Redis 7 | BSD-3-Clause | Container in `deploy/compose`. Pinned to the 7.x line, which predates the licence change. |

## Marketing site

npm dependencies in `package.json` are MIT-licensed shadcn/Radix/Vite ecosystem
packages plus vinext and the OpenAI Sites plugin; the site is a static export
and none of them ship in `compute-api`.

### Fonts (self-hosted, shipped to every visitor)

The site serves these font files from its own origin rather than from Google
Fonts, because loading from Google sends visitors' IP addresses to Google
(UnifyAI UI standard). The `@fontsource` packages bundle the woff2 files into
the build, and each package carries the font's licence text in its `LICENSE`.

| Font | Weights | Package | Licence |
|---|---|---|---|
| Space Grotesk | 500, 700 | `@fontsource/space-grotesk` 5.3.0 | SIL Open Font License 1.1 |
| Instrument Sans | 400, 500, 600 | `@fontsource/instrument-sans` 5.3.0 | SIL Open Font License 1.1 |
| IBM Plex Mono | 400, 500 | `@fontsource/ibm-plex-mono` 5.3.0 | SIL Open Font License 1.1 |

OFL allows bundling and redistribution with the site. It forbids selling the
fonts by themselves, and a modified font may not use the reserved font name.
We do neither. Email templates (`docs/spec/email/base.html`) link the same
three fonts from Google Fonts, as the shared email kit specifies, because mail
clients do not load self-hosted fonts reliably.

### Brand artwork

`public/brand/`, `public/email/`, `public/icons/`, the favicons and
`public/og-image-v3.png` are UnifyAI's own artwork from the Option 01 "Inward
hex" logo package and the UnifyAI brand kit. They are proprietary and are not
third-party material.
