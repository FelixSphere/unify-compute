// The marketplace pages are generated at build time from the same files
// compute-api serves (ADR-0012, docs/spec/marketplace.md). A change to either
// file therefore needs a site release as well as an API release.
import skuFile from '../../compute-api/config/skus.json';
import templateFile from '../../compute-api/config/templates.json';
import { COMPUTE_API } from '../links';

type Sku = (typeof skuFile.skus)[number];
type Mount = { mount: string; description: string };
type RawTemplate = {
  id: string;
  title: string;
  description: string;
  image: string;
  image_licence?: string;
  image_source?: string;
  env?: Partial<Record<string, string>>;
  sku: string;
  max_runtime_seconds: number;
  inputs: Mount[];
  outputs: Mount[];
  overridable: string[];
  available: boolean;
  unavailable_reason?: string;
};

export type Listing = RawTemplate & {
  gpu: Pick<Sku, 'gpu' | 'gpu_count' | 'gpu_memory_gb'>;
  runnable: boolean;
  reason?: string;
};

// ADR-0012 §2: the buyer sees the marketplace, not the author.
export const PUBLISHER = 'Unify Compute';

export const OVERRIDE_LABELS: Record<string, string> = {
  sku: 'GPU (SKU)',
  providers: 'Cloud placement',
  'spec.command': 'Command',
  'spec.env': 'Environment variables',
  'spec.max_runtime_seconds': 'Maximum runtime',
};

const skus = new Map(skuFile.skus.map((s) => [s.id, s]));

// The same rule as compute-api's template package: declared available AND
// the default SKU is sold (docs/spec/templates.md, "Availability").
function toListing(t: RawTemplate): Listing {
  const sku = skus.get(t.sku);
  if (!sku) throw new Error(`template ${t.id}: SKU ${t.sku} is not in skus.json`);
  const gpu = { gpu: sku.gpu, gpu_count: sku.gpu_count, gpu_memory_gb: sku.gpu_memory_gb };
  if (!t.available) return { ...t, gpu, runnable: false, reason: t.unavailable_reason };
  if (!sku.available) return { ...t, gpu, runnable: false, reason: `Its GPU (${sku.id}) is not available yet.` };
  return { ...t, gpu, runnable: true };
}

// Assigned, not asserted, so a field whose JSON type drifts fails tsc.
const raw: RawTemplate[] = templateFile.templates;

export const listings: Listing[] = raw
  .map(toListing)
  .sort((a, b) => Number(b.runnable) - Number(a.runnable) || a.id.localeCompare(b.id));

export function findListing(id: string) {
  return listings.find((l) => l.id === id);
}

export function gpuLabel({ gpu, gpu_count, gpu_memory_gb }: Listing['gpu']) {
  return `${gpu_count > 1 ? `${gpu_count} × ` : ''}${gpu} · ${gpu_memory_gb} GB`;
}

export function runtimeLabel(seconds: number) {
  if (seconds % 3600 === 0) return `${seconds / 3600} h`;
  return `${Math.round(seconds / 60)} min`;
}

// The first sentence, for cards. Descriptions are written as prose by the
// template's reviewer, so a sentence boundary is ". " followed by a capital.
export function summary(description: string) {
  const m = description.match(/^.+?\.(?=\s+[A-Z])/);
  return m ? m[0] : description;
}

// The run step saves an input under its URL's last path segment, and the
// command reads a fixed file name, which each input's description names
// (docs/spec/templates.md). An example URL without that name would be refused
// or would run against nothing, so a template whose description names no file
// fails the build rather than publishing a wrong example.
function inputFile(listing: Listing, input: Mount) {
  const m = input.description.match(/[\w-]+\.(?:jsonl|json|tar\.gz|tgz)\b/);
  if (!m) throw new Error(`template ${listing.id}: input ${input.mount} names no file for the run example`);
  return m[0];
}

export function runExample(listing: Listing) {
  const body = {
    template: listing.id,
    spec: {
      inputs: listing.inputs.map((input) => ({
        source: `https://YOUR-BUCKET.s3.amazonaws.com/${inputFile(listing, input)}`,
        mount: input.mount,
      })),
    },
  };
  return `curl -s ${COMPUTE_API}/jobs \\
  -H "Authorization: Bearer $UNIFY_KEY" \\
  -H "Content-Type: application/json" \\
  -d '${JSON.stringify(body, null, 2)}'`;
}

export function imageParts(image: string) {
  const [ref, digest] = image.split('@');
  return { ref, digest };
}
