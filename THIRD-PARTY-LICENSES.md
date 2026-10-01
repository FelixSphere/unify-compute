# Third-party licences

Every dependency of `compute-api/` and every component `deploy/` runs is listed
here, in the commit that adds it. Permissive licences only (ADR-0002).

## compute-api (linked into our binary)

| Component | Licence | Notes |
|---|---|---|
| Go standard library | BSD-3-Clause | No third-party modules yet. `go.mod` has no `require` block; keep it that way until something earns its place. |

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
