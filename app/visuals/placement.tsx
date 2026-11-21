import type { Gpu } from '../catalogue';
import { Conveyor } from './conveyor';

// How a SKU is placed (ADR-0006): the customer picks the SKU; its catalogue
// entry lists cloud placements in preference order; the job launches into
// the first with capacity. Drawn from skus.json, so it names only the clouds
// a sold SKU really lists. Empty dashed rows stand for "next in order" and
// carry no name.
export function Placement({
  sku,
  labels,
  summary,
}: {
  sku: Gpu;
  labels: { sku: string; placements: string; gpu: string; tier: string };
  summary: string;
}) {
  const rows = sku.providers.map((p) => `${p.cloud} · ${p.instance_type} · ${p.regions.join(", ")}`);
  const empties = Math.max(0, 3 - rows.length);

  return (
    <div className="bg-surface mt-14 rounded-2xl border p-6 sm:p-10">
      <div aria-hidden className="flex flex-col items-stretch md:flex-row md:items-center">
        <div className="min-w-0 md:w-[28%] md:shrink-0">
          <p className="panel-caption mb-3">{labels.sku}</p>
          <div className="bg-amber text-ink rounded-xl px-5 py-5">
            <code dir="ltr" className="block truncate font-mono text-sm font-medium lg:text-base">
              {sku.id}
            </code>
            <span dir="ltr" className="mt-2 block text-sm">
              {sku.gpu} · {sku.gpu_memory_gb} GB
            </span>
          </div>
        </div>

        <Conveyor vertical className="md:hidden" packets={2} duration={2.4} />
        <Conveyor className="hidden flex-1 md:block" packets={2} duration={2.4} />

        <div className="min-w-0 md:w-[38%] md:shrink-0">
          <p className="panel-caption mb-3 flex justify-between gap-3">
            <span>{labels.placements}</span>
            <span>{labels.tier}</span>
          </p>
          <ol className="m-0 grid list-none grid-cols-[minmax(0,1fr)] gap-2 p-0">
            {rows.map((row, i) => (
              <li key={row} className="bg-ink text-paper relative flex h-12 items-center gap-3 rounded-lg px-4">
                <span className="text-on-dark font-mono text-xs">{i + 1}</span>
                <code dir="ltr" className="min-w-0 truncate font-mono text-sm">
                  {row}
                </code>
                {i === 0 && <span className="animate-lock border-amber absolute -inset-1 rounded-xl border-[3px]" />}
              </li>
            ))}
            {Array.from({ length: empties }, (_, i) => (
              <li key={i} className="border-ink/20 flex h-12 items-center gap-3 rounded-lg border-2 border-dashed px-4">
                <span className="text-muted-foreground font-mono text-xs">{rows.length + i + 1}</span>
              </li>
            ))}
          </ol>
        </div>

        <Conveyor vertical className="md:hidden" packets={2} duration={2.4} />
        <Conveyor className="hidden flex-1 md:block" packets={2} duration={2.4} />

        <div className="min-w-0 md:shrink-0">
          <p className="panel-caption mb-3 text-center">{labels.gpu}</p>
          <div className="animate-gpu-on bg-ink text-paper mx-auto grid h-24 w-24 grid-cols-3 gap-2 rounded-2xl p-4 md:h-28 md:w-28">
            {Array.from({ length: 9 }, (_, i) => (
              <span key={i} className="rounded-[3px] bg-current" />
            ))}
          </div>
        </div>
      </div>
      <p className="sr-only">{summary}</p>
    </div>
  );
}
