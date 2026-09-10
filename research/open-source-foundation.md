# unifyAI: open-source foundation decision

Researched 10 September 2026. Decision: fork **SkyPilot** as the compute orchestration foundation.

Verified fork: https://github.com/FelixSphere/skypilot
Upstream: https://github.com/skypilot-org/skypilot
License reported by upstream and GitHub: Apache-2.0.
Latest stable release observed: v0.13.0, published 22 July 2026.

## Why SkyPilot

unifyAI spans on-demand GPU jobs, batch inference, embeddings, document pipelines, and fine-tuning. Its hardest shared requirement is provisioning and managing compute across these workloads. SkyPilot is the closest match at that layer: it has cloud and Kubernetes resource provisioning, managed jobs, recovery, idle cleanup, and SkyServe model serving. The recommendation is a product-fit judgment based on the upstream capabilities, not a production benchmark.

Reuse the fork's orchestration and provider integrations. Treat it as an upstream-compatible foundation, pin a tested release for deployment, and keep unifyAI's customer product in a separate service so upstream updates stay manageable.

## Alternatives evaluated

| Project | Useful capabilities | Why it is not the single primary fork |
| --- | --- | --- |
| [SkyPilot](https://github.com/skypilot-org/skypilot) | GPU/cloud provisioning, job lifecycle, recovery, serving | Selected: broadest match to on-demand compute across all four workloads |
| [Ray](https://github.com/ray-project/ray) | Distributed execution, data processing, training, serving | Strong workload runtime; a larger execution framework to adopt when the immediate need is provisioning arbitrary jobs |
| [BentoML](https://github.com/bentoml/BentoML) | Model packaging, inference APIs, batching, pipelines | Strong serving layer, less direct as the foundation for general GPU job provisioning |
| [vLLM](https://github.com/vllm-project/vllm) | Efficient LLM inference and serving | Useful as a dependency inside jobs; does not supply the broad job, training, and commercial platform layer |

No other projects were forked.

## What we can reuse

- Managed job submission, execution state, resource provisioning, logs, and retry/recovery mechanics.
- Resource selection and multi-cloud/Kubernetes integrations.
- Automatic resource cleanup; verify exact controller and backend behavior when configuring costs.
- SkyServe for supported inference deployments. Model runtimes and document processors are separate workload dependencies.

Sources: [managed jobs](https://docs.skypilot.ai/en/stable/examples/managed-jobs.html), [autostop](https://docs.skypilot.ai/en/stable/reference/auto-stop.html), [upstream overview](https://github.com/skypilot-org/skypilot), [release](https://github.com/skypilot-org/skypilot/releases/tag/v0.13.0).

## What unifyAI still needs

1. Customer identity, organizations, API keys, scoped authorization, and rate limits.
2. Tenant isolation, workload policy, secrets management, egress controls, and resource quotas. Team access features do not by themselves establish safe isolation for mutually untrusted customers.
3. A durable consumption ledger: immutable job/resource events, measured GPU time, inference units where applicable, and reconciliation against cloud invoices. Provider cost estimates are not a customer billing ledger.
4. Pricing rules, payment integration, invoices, credit limits, refunds, and retry-aware charging.
5. Curated workload containers for embeddings, extraction/OCR, batch inference, and fine-tuning, with model-license review and artifact storage.
6. A customer console, job API, result delivery, cancellation, monitoring, and operational support.

SkyPilot deploys into cloud accounts or clusters; the fork does not supply GPUs. No cloud compute, billing, or production inference endpoint has been provisioned in this task.

## Suggested first implementation

Start with one GPU provider and one batch inference job type. Put an authenticated unifyAI job API in front of SkyPilot, persist job state and idempotency keys, write usage events to a durable ledger, and store results in object storage. Validate cancellation, retries, interrupted jobs, tenant boundaries, and metering reconciliation before adding payments or more workload types.

Keep upstream license and notice files intact. Audit workload/container dependencies separately. The fork remains unmodified upstream source; this task selects and forks the foundation, rather than implementing the compute backend.
