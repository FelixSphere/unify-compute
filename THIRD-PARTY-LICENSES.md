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
| [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto) v0.42.0 | BSD-3-Clause | Direct, `internal/accounts`: argon2id password hashing (`argon2`, ADR-0011 §8). v0.42.0 is the newest release whose `go.mod` accepts Go 1.24 without raising `golang.org/x/text` past the version pgx already uses. |
| [golang.org/x/sync](https://pkg.go.dev/golang.org/x/sync) v0.17.0 | BSD-3-Clause | Transitive, via puddle (`semaphore`). |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) v0.36.0 | BSD-3-Clause | Transitive, via `x/crypto/blake2b` (inside argon2): CPU feature detection (`cpu`). |
| [golang.org/x/text](https://pkg.go.dev/golang.org/x/text) v0.29.0 | BSD-3-Clause | Transitive via pgx (SASLprep for SCRAM passwords), and direct in `internal/accounts`: NFC normalisation of email addresses (`unicode/norm`). |

The modules above are what `go list -deps ./...` reaches; `go.mod` lists
nothing else. pgx's own test dependencies (testify and friends) are not in
our build graph. The `cmd/compute-api` binary links pgx and its transitive
modules through `internal/quota`, which imports `internal/store` for its
usage types; the binary does not open a database yet.

## Run alongside, over HTTP or as a separate container (not linked)

| Component | Licence | Relationship |
|---|---|---|
| [SkyPilot](https://github.com/skypilot-org/skypilot) via `FelixSphere/skypilot` | Apache-2.0 | Orchestration backend, driven through its API server. Carries a NOTICE obligation for whichever packaging ships it. |
| [new-api](https://github.com/QuantumNous/new-api) via `FelixSphere/unifyapi-console` | AGPL-3.0 | Account plane. HTTP only. Nothing from it is copied into this repository; see ADR-0002. The fork's own source stays public. |
| PostgreSQL | PostgreSQL Licence | Container in `deploy/compose`. |
| Redis 7 | BSD-3-Clause | Container in `deploy/compose`. Pinned to the 7.x line, which predates the licence change. |

## Release tooling (runs in CI, never shipped)

| Component | Licence | Relationship |
|---|---|---|
| [crane](https://github.com/google/go-containerregistry) v0.22.1 (go-containerregistry), release binary `go-containerregistry_Linux_x86_64.tar.gz`, pinned by SHA-256 | Apache-2.0 | `.github/workflows/release.yml` downloads it to copy released image digests from GHCR to ECR unchanged (ADR-0009, "Runtime registry: ECR"). Run, not linked or redistributed; the upstream repository has no NOTICE file. |

## Marketing site

npm dependencies in `package.json` are MIT-licensed shadcn/Radix/Vite ecosystem
packages plus vinext (MIT); the site is a static export and none of them ship
in `compute-api`. The OpenAI Sites plugin, wrangler and the Cloudflare Vite
plugin were removed when the build became a static export.

### Site container (`deploy/site/`)

| Component | Licence | Relationship |
|---|---|---|
| [Caddy](https://github.com/caddyserver/caddy) 2.11.4, official `caddy:2.11.4-alpine` image, pinned by digest | Apache-2.0 | Runtime base of the `site` image: serves the static export. Unmodified; no NOTICE file in the upstream repository. |
| Node.js 22, official `node:22.23.3-alpine` image, pinned by digest | MIT | Build stage only. Nothing from it is in the published image. |

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

## Workload images

Images jobs run in. Not linked into `compute-api/`; listed because we build
and publish this one (ADR-0002 keeps the permissive-only rule for anything we
ship). The base image is the vLLM image recorded above for the batch templates.

| Component | Licence | Relationship |
|---|---|---|
| `vllm/vllm-openai:v0.30.0-cu129@sha256:a67f8f18…` | Apache-2.0 | Base of `finetune/Dockerfile`; carries torch, transformers and vLLM. |
| [TRL](https://github.com/huggingface/trl) 1.14.1 | Apache-2.0 | `train.py`: `SFTTrainer`. |
| [PEFT](https://github.com/huggingface/peft) 0.21.2 | Apache-2.0 | `train.py`: LoRA (`LoraConfig`); vLLM loads the resulting adapter. |
| [datasets](https://github.com/huggingface/datasets) 5.0.1 | Apache-2.0 | `train.py`: JSONL loading. |
| [accelerate](https://github.com/huggingface/accelerate) 1.15.0 | Apache-2.0 | Required by TRL's trainer. |
| [jsonschema](https://github.com/python-jsonschema/jsonschema) 4.26.0 | MIT | `eval.py`: schema validity metric. |
| Qwen3-4B-Instruct-2507 (default base model, downloaded at run time) | Apache-2.0 | Not in the image; pinned by revision in the templates' env. |
