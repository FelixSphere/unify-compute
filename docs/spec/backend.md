# Job backend: running jobs on SkyPilot

- **Status:** Spec + partial. `compute-api/internal/backend/` implements
  launch, observation, logs, cancel and teardown against the SkyPilot API
  server, and the lifecycle events below. `internal/jobs` wires it to
  `POST /compute/v1/jobs`, the ledger (C5) and the event log: its store
  writes each status change with its event, its sink feeds the ledger, and
  the ledger's stop requests reach `Backend.Stop`. Nothing has been launched
  on a real cloud.
- Task: C4 in [`product-design.md`](../product-design.md).
- Depends on: [ADR-0003](../adr/0003-skypilot-orchestration-foundation.md)
  (SkyPilot through its API server, pinned to v0.13.0),
  [ADR-0006](../adr/0006-upstreams-are-skypilot-clouds.md) and
  [`upstreams.md`](upstreams.md) (R8 placement, R10 placement record, R15
  isolation), [`job-api.md`](job-api.md) (states), and
  [`metering.md`](metering.md) (which edges start and stop the GPU meter).

## Decision: clusters, not managed jobs

Each job attempt is **one SkyPilot cluster** (`POST /launch` with a fresh
cluster name), observed by compute-api and torn down by compute-api
(`POST /down`). We do not use managed jobs (`POST /jobs/launch`).

Why:

1. **Metering needs the attempt boundaries, and managed jobs hide them.**
   GPU seconds run from cluster `UP` to the end event, per attempt, and
   provisioning is never billed, including a retry's (metering.md I1, B4).
   A managed job recovers from preemption inside its controller: it
   relaunches and fails over on its own, reporting only job-level timestamps
   and a counter (`ManagedJobRecord.start_at`, `end_at`, `recovery_count`,
   `last_recovered_at`, `sky/schemas/api/responses.py:216-225`; status
   `RECOVERING`, `sky/jobs/state.py:529`). We would have to reconstruct when
   the replacement cluster came `UP` from outside. On a plain cluster we
   observe `UP` directly.
2. **Retry is ours to decide.** `max_runtime_seconds` caps billed seconds
   *across* attempts, and a retry that would exceed it must not start (I5).
   A managed job's controller retries without asking. Retry is not built
   yet (job-api.md: *planned*). With clusters it will be an explicit new
   attempt, `ClusterName(job, n+1)`, rather than something SkyPilot does
   behind our back.
3. **One fewer moving part.** Managed jobs run a jobs controller (a VM, pod,
   or a process inside the API server in consolidation mode). It has its own
   lifecycle and cost, and it would be a second source of truth for job
   state.

What it costs us: compute-api owns teardown and must not leak clusters. That
is handled by explicit `/down` on every end, with retries until SkyPilot
confirms, plus SkyPilot's own autodown as a backstop (below). If retry gets
built and managed-job recovery turns out to be what we want after all, that
change needs its own ADR. ADR-0003's choice of the API server over the CLI
holds either way.

## Wire contract (SkyPilot v0.13.0)

Everything here was read from the source of the pin,
`FelixSphere/skypilot` at tag `v0.13.0` (commit `b1431e5`, "Release 0.13.0").
It was then exercised against a local 0.13.0 API server (`sky api start`
with an empty `HOME`: no cloud credentials, so nothing could launch).
SkyPilot publishes no REST reference for the API server. The Python SDK is
the only documented client, so the citations are to source lines.

**Request model.** Every operation is asynchronous. A `POST` answers `200`
at once with the request id in the `X-Skypilot-Request-ID` header
(`sky/server/server.py:228`). `GET /api/get?request_id=` blocks until the
request finishes and returns `payloads.RequestPayload`
(`sky/server/requests/payloads.py:1005`). On failure it answers `500` with
`{"detail": RequestPayload}` (`server.py:2384`). `503` means the request has
to be resubmitted (`server.py:2381`), and `404` means an unknown id.
`return_value` and `error` are JSON documents *encoded as strings*. `error` is
`{"object": <pickle>, "type": <exception class>, "message": str(e)}`
(`sky/server/requests/requests.py:209`). Messages carry ANSI colour codes,
which the client strips. `GET /api/status?request_ids=&all_status=true`
(`server.py:2637`) is the non-blocking view. Request states are
`PENDING, WAITING, RUNNING, SUCCEEDED, FAILED, CANCELLED`
(`requests.py:82`).

**Headers.** `X-SkyPilot-API-Version: 56` and `X-SkyPilot-Version: 0.13.0`
(`sky/server/constants.py:13,28,31`). Without them the server falls back to
its pre-0.11 compatibility path (`APIVersionMiddleware`, `server.py:984`).
`Authorization: Bearer <service-account token>` when the server has auth.

**Common body fields.** Every body is a `payloads.RequestBody`
(`payloads.py:150`). `env_vars` is sent explicitly with `SKYPILOT_USER_ID`
and `SKYPILOT_USER`. Pydantic would otherwise fill it from the *server's* own
environment (`payloads.py:175`), and the clusters would belong to whoever
started the server. All clusters belong to one service identity, so any
compute-api replica can observe and tear down any job's cluster.

| Use | Call | Body (`payloads.py`) | Result |
|---|---|---|---|
| Launch an attempt | `POST /launch` (`server.py:1842`) | `LaunchBody` (`:289`): `task` (task YAML as a string; JSON is sent, which is valid YAML, `dag_utils.py:190`), `cluster_name`, `retry_until_up: false` | `{"job_id": int, "handle": <pickle>}` (`encoders.py:105`) |
| Observe the cluster | `POST /status` (`:1895`) | `StatusBody` (`:377`): `cluster_names`, `refresh: "AUTO"`, `all_users: true`, `include_credentials: false` (default `true` returns SSH keys, `:383`) | list of `StatusResponse` (`responses.py:99`, `encoders.py:76`); a missing cluster is absent |
| Observe the job | `POST /queue` (`:1978`) | `QueueBody` (`:411`) | list of `ClusterJobRecord` (`responses.py:157`, `encoders.py:121`); `ClusterDoesNotExist` / `ClusterNotUpError` otherwise |
| Logs | `POST /logs` (`:2023`) | `ClusterJobBody` (`:453`): `job_id`, `follow: false`, `tail: 0` | streamed `text/plain` (`stream_utils.py:443`), interleaved with `rich_utils.Control` lines (`rich_utils.py:56`) that are dropped |
| Stop a launch in flight | `POST /api/cancel` (`:2623`) | `RequestCancelBody` (`:693`): `request_ids` | list of killed ids |
| Tear down | `POST /down` (`:1933`) | `StopOrDownBody` (`:370`) | `null`; `ClusterDoesNotExist` if already gone |
| Find a launch after a lost answer | `GET /api/status?cluster_name=&all_status=true` | — | requests for that cluster, newest by `created_at` |

Observed against the real server and not obvious from reading the source:

- A `/logs` of a missing cluster answers **`200` with an empty body**. The
  `ClusterDoesNotExist` is on the request named in the response header, so
  the client reads that request after the stream ends.
- File-mount sources must not end in `/` ("Mount paths cannot end with a
  slash"), although job-api.md writes prefixes that way. Input sources are
  trimmed.
- S3 `MOUNT` and `MOUNT_CACHED` accept only a **whole bucket**
  (`sky/data/storage.py:1221-1237`, `StorageModeError`). See open question 1.

The recorded responses are in
`compute-api/internal/backend/testdata/skypilot-0.13.0/`. The pickled Python
objects in them, which embed stack-trace paths, are replaced by a placeholder.

## Job → SkyPilot task

| Job | Task | Note |
|---|---|---|
| `id`, attempt | `name` and cluster name `uc-<lowercased ULID>-<attempt>` | One cluster per attempt: R15 (one job per VM), and the name is the only handle needed to find the attempt again |
| SKU + candidates | `resources` from `skypilot.ResourcesFor` (R8): flat for one candidate, `any_of` in catalogue order for several | Shared fields below sit beside `any_of` and apply to every candidate |
| `spec.image` | `resources.image_id: docker:<image>` | |
| `spec.command` | `run`: argv shell-quoted so it reaches the process unchanged under `bash -c` | |
| `spec.env` | `envs` | Names checked against SkyPilot's `^[a-zA-Z_][a-zA-Z0-9_]*$` |
| `spec.inputs` | `https://` source: fetched with curl into the `mount` directory, under the URL's last path segment, at the start of `run`; a failed fetch stops the command. Any other source: refused with `TaskOptions.HTTPSInputsOnly` (`ErrInputSource`), which a deployment sets (ADR-0005); without it, `file_mounts: {mount: source}` (COPY) | SkyPilot reads an `https://` file mount as Azure Blob (`sky/cloud_stores.py:1053`). Any other source would be copied with SkyPilot's credentials, which are ours. The `file_mounts` path stays for the recorded goldens and the cross-account role. A customer bucket is never mounted read-write |
| `spec.outputs` | With `TaskOptions.Outputs` set (`internal/artifacts`): `run` becomes the upload script, `envs` gains `UNIFY_COMPUTE_ARTIFACTS_URL` and `UNIFY_COMPUTE_ARTIFACTS_TOKEN`. Without it: **refused** (`ErrOutputsUnsupported`) | ADR-0005. Never a file mount. Customer env names starting `UNIFY_COMPUTE_` are refused (`ErrReservedEnv`) |
| `max_runtime_seconds` | enforced by compute-api (below) | Not in the task |
| teardown | `resources.autostop: {idle_minutes: 5, down: true}` (`schemas.py:128`) | Backstop only |

`retry_until_up` is false. Waiting for capacity indefinitely would hold the
customer in `provisioning` with no bound. Exhausting every candidate fails
the job (job-api.md, `provisioning → failed`).

## State mapping

The backend steps each attempt: one `/status` (and `/api/status` while the
launch is in flight, `/queue` once `UP`) per observation. `Run` does this on a
ticker. Tests call `Step` directly.

| Observation | Job transition | `ended` reason / `error.code` | Retryable |
|---|---|---|---|
| `Launch` accepted | `queued → provisioning` | — | — |
| Cluster `UP` (first observation) | `provisioning → running`, placement written first | — | — |
| Queue job `RUNNING` | — (`job_started` event) | — | — |
| Queue job `SUCCEEDED` | `→ succeeded` | `exited` | — |
| Queue job `FAILED` / `FAILED_SETUP` | `→ failed` | `exited` / `nonzero_exit` | no |
| Queue job `FAILED_DRIVER` | `→ interrupted` | `provider_fault` / `provider_fault` | yes |
| Launch `FAILED`, `ResourcesUnavailableError`, before `UP` | `provisioning → failed` | `provision_failed` / `capacity_exhausted` | yes |
| Launch `FAILED`, message names `docker pull` / `ImagePullBackOff` / `ErrImagePull` / `manifest unknown` … | `→ failed` | `provision_failed` / `image_pull_failed` | no |
| Launch `FAILED`, interruptible SKU, message mentions preemption, before `UP` | `provisioning → interrupted` | `provision_failed` / `preempted` | yes |
| Launch `FAILED`, anything else, before `UP` | `provisioning → failed` | `provision_failed` / `provision_failed` | yes, except a refused task |
| Launch `FAILED` after `UP` (file mounts, setup) | `running → failed` | `exited` / classified as above | as above |
| Cluster absent or not `UP` after `UP`, interruptible SKU | `running → interrupted` | `preempted` / `preempted` | yes |
| … on-demand SKU | `running → interrupted` | `provider_fault` / `provider_fault` | yes |
| `UP` + `max_runtime_seconds` reached | `running → failed` | `max_runtime` / `max_runtime_exceeded` | no |
| `Cancel` | `→ cancelled` | `cancelled` | — |

Every end tears the cluster down with `/down`, cancelling the launch request
first if it is still running. Autodown is not enough on its own: SkyPilot
leaves a cluster that failed provisioning or setup up "for debugging
purposes" (`sky/execution.py:241`), and that cluster bills us. A `/down` that
fails is retried on the next step. `ClusterDoesNotExist` counts as down.

Image pull failure has no exception type of its own. On a VM it is a
`CommandError` around `docker pull` (`sky/provision/docker_utils.py:356`); on
Kubernetes it is the pod's waiting reason (`kubernetes/utils.py:2030`). It is
therefore matched on the message, and checked before
`ResourcesUnavailableError`, because a pull that fails on every candidate
also ends as "no resources".

## Lifecycle events (for C5)

```go
type Event struct {
    ID         string     // "<job_id>/<attempt>/<kind>": deterministic, dedupe on it
    JobID      string
    Attempt    int
    Kind       EventKind  // provisioning | cluster_up | job_started | ended | cluster_down
    At         time.Time  // when the edge happened, by the rule below
    ObservedAt time.Time  // when compute-api learned of it
    Placement  *Placement // {cloud, region, instance_type}; from cluster_up on
    UpAt       *time.Time // on ended: the attempt's cluster_up At; nil = never UP, zero GPU seconds
    Reason     EndReason  // ended: exited | cancelled | max_runtime | preempted | provider_fault | provision_failed
    Status     store.Status // ended: the terminal job status
    Failure    *Failure   // ended: {Code, Message, Retryable}; nil on success and customer cancel
}

type Sink interface { Emit(ctx context.Context, e Event) error }
```

| Kind | `At` | Meter |
|---|---|---|
| `provisioning` | launch handed to SkyPilot | none (I1) |
| `cluster_up` | compute-api's first observation of `UP` (metering.md "Start"; SkyPilot's own `status_updated_at` is not used) | **GPU starts** |
| `job_started` | the node's `start_at` from `/queue` | informational |
| `ended` | process exit: the node's `end_at` (rule 1). Cancel: the time passed to `Cancel`, i.e. the API's acceptance (rule 2, B11). Max runtime: exactly `UpAt + max_runtime_seconds` (rule 3, B16). Preemption or fault: the **last observation of `UP`**, because SkyPilot reports no preemption time (rule 4, I3). Pre-`UP` failure: when observed | **GPU stops** |
| `cluster_down` | when SkyPilot finished the `/down` request | none; the gap after `ended` is ours |

`ended` is emitted exactly once per attempt, whether or not it was ever
`UP`, so C5 can settle every attempt from that one event.

Durability: each edge goes to the sink **before** the store write. A failing
`Emit` stops the attempt at that edge, and the same event, with the same `ID`
and `At`, is emitted again on the next step. The job does not read `running`
until its `cluster_up` is in the ledger. A consumer has to tolerate seeing an
event twice. Events are never reordered or dropped.

`placement` (R10) is written with `store.SetPlacement` before
`provisioning → running`, so a running job always has one. A retried attempt
replaces it. A terminal job refuses it (`store.ErrTerminal`).

## Max runtime

compute-api enforces it: on the first step at or after
`UpAt + max_runtime_seconds` the attempt ends, billed to exactly the cap, and
is torn down. The cap is judged on when things *happened*, not when they were
observed. A job that exited before its cap but was seen after it still
succeeds. An exit, preemption or failure whose time falls after the cap
becomes `max_runtime` at the cap. The task carries no in-cluster timeout, because a `timeout`
wrapper would depend on coreutils being in the customer's image. Open
question 3 covers the case where compute-api is not running.

## Tests

Docker is not available on the machine this was built on, so the
kind-cluster integration test in product-design.md's C4 row was not possible.
Instead:

- `skypilottest`: an in-process fake of the endpoints above, with cluster
  and job lifecycle scripted by the test. It refuses calls without the
  version headers, the token, or `SKYPILOT_USER_ID`, and refuses a
  `/status` that would return credentials.
- Recorded responses from the real 0.13.0 server pin the decoding. A parity
  test fails if the fake's payload fields drift from the recording. The task
  goldens are byte-for-byte what that server accepted.
- `TestRealAPIServer`, gated on `SKYPILOT_API_SERVER_URL`, runs the client
  and the whole launch, fail and teardown loop against a real server. The
  launch half runs only if the server reports **no enabled cloud**, so it can
  never provision anything. It passed against a local 0.13.0 server.
- `TestBackendWithPostgresStore` (gated on `COMPUTE_TEST_DATABASE_URL`) runs
  one job through the real store.

## Open questions

1. **Outputs.** *Proposed answer in ADR-0005:* the run step uploads the
   output mounts from an EXIT trap, with curl and one presigned PUT per file
   from compute-api, so the node holds no AWS credentials and nothing is
   mounted. The upload runs inside the SkyPilot job, so it finishes before
   the backend sees the job end and calls `/down`. A job ended by cancel,
   preemption or `max_runtime_seconds` is torn down without uploading.
   Files over 5 GiB, which R1's merged weights would be, need multipart
   (ADR-0005, open question 1).
2. **Restart.** Attempt state, including `UpAt`, is in memory. After a
   compute-api restart the cluster can be found by name and the launch by
   `/api/status?cluster_name=`, but `UpAt` has to come back from the ledger
   (C5's `cluster_up` row). The wiring (`internal/jobs`) did not add that
   `Resume`; it is still needed. Until it exists a restart loses track of
   live attempts: the binary logs each non-terminal job it finds at boot as
   "not resumed", the job reads its last status indefinitely, its metering
   stops at the last observed `UP`, and its hold stays locked until the
   console's sweeper expires it (H6). SkyPilot's autodown
   reaps their clusters 5 minutes after the job ends.
3. **Max runtime without compute-api.** If compute-api is down past a job's
   cap, the job keeps running at our cost (the customer's bill is still
   capped by B16). An in-cluster backstop needs either a `timeout` the image
   may not have, or SkyPilot autostop on wall clock, which it does not offer.
4. **Logs after teardown.** `/logs` needs the cluster. Keeping logs for 7
   days (job-api.md) needs `/download_logs` before `/down`, plus somewhere to
   put them (same storage question as 1).
5. **Polling cost.** `refresh: AUTO` calls the cloud's API on every step for
   every live cluster. Batching all clusters into one `/status` per tick is
   straightforward; whether cloud API rate limits force it is not known
   until there are real jobs.
6. **Pre-start preemption** is recognised only by the word "preempt" in the
   launch error of an interruptible SKU. That has not been seen on a real
   spot launch. It needs checking against the first real one, along with
   the image-pull markers and how `cloud` and `region` read on non-AWS
   clouds.
7. **`FAILED_DRIVER` as `interrupted`.** This assumes a lost driver is the
   node's fault rather than the customer's. It needs confirming on a real
   failure before it decides who pays.
