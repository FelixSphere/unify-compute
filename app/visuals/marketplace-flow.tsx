import { Conveyor } from './conveyor';

// How a template reaches a GPU (ADR-0012): an image, a command and a GPU are
// put under one name, the template is reviewed before it is listed, and a
// buyer runs a listed one with their own key. The shelf shows real template
// ids from templates.json.
export function MarketplaceFlow({
  ids,
  labels,
  summary,
}: {
  ids: string[];
  labels: { image: string; command: string; gpu: string; reviewed: string; listed: string; job: string };
  summary: string;
}) {
  const parts = [
    { label: labels.image, className: 'bg-ink text-paper' },
    { label: labels.command, className: 'bg-ink text-paper' },
    { label: labels.gpu, className: 'bg-amber text-ink' },
  ];
  return (
    <div className="bg-surface mt-14 rounded-2xl border p-6 sm:p-10">
      <div aria-hidden className="flex flex-col items-stretch lg:flex-row lg:items-center">
        <div className="mx-auto grid w-full max-w-[220px] min-w-0 gap-2 lg:mx-0 lg:w-[18%] lg:shrink-0">
          {parts.map((p) => (
            <span key={p.label} className={`${p.className} flex h-11 min-w-0 items-center justify-center truncate rounded-lg px-3 text-sm font-semibold`}>
              {p.label}
            </span>
          ))}
        </div>

        <Conveyor vertical className="lg:hidden" packets={2} duration={2.6} />
        <Conveyor className="hidden flex-1 lg:block" packets={2} duration={2.6} />

        <div className="flex min-w-0 flex-col items-center lg:shrink-0">
          <span className="bg-ink text-amber relative flex h-24 w-24 items-center justify-center rounded-2xl">
            <span className="animate-ring border-amber absolute inset-0 rounded-2xl border-[3px]" />
            <svg viewBox="0 0 24 24" className="h-12 w-12" fill="none">
              <path d="M4 12.5 L10 18 L20 6" pathLength={1} className="animate-draw" stroke="currentColor" strokeWidth="3.5" strokeLinecap="round" strokeLinejoin="round" />
            </svg>
          </span>
          <span className="mt-3 text-sm font-semibold">{labels.reviewed}</span>
        </div>

        <Conveyor vertical className="lg:hidden" packets={2} duration={2.6} />
        <Conveyor className="hidden flex-1 lg:block" packets={2} duration={2.6} />

        <div className="mx-auto w-full max-w-[300px] min-w-0 lg:mx-0 lg:w-[30%] lg:shrink-0">
          <p className="panel-caption mb-3">{labels.listed}</p>
          <ul className="m-0 grid list-none gap-2 p-0">
            {ids.map((id, i) => (
              <li key={id} className="bg-paper relative flex h-11 min-w-0 items-center gap-3 rounded-lg border px-3">
                <span className="bg-amber h-5 w-5 shrink-0 rounded-[4px]" />
                <code dir="ltr" className="min-w-0 truncate font-mono text-sm">
                  {id}
                </code>
                <span className="animate-shelf absolute -inset-[3px] rounded-[10px] border-[3px] border-transparent" style={{ animationDelay: `${i * 2}s` }} />
              </li>
            ))}
          </ul>
        </div>

        <Conveyor vertical className="lg:hidden" packets={2} duration={2.6} />
        <Conveyor className="hidden flex-1 lg:block" packets={2} duration={2.6} />

        <div className="flex min-w-0 flex-col items-center lg:shrink-0">
          <span className="bg-ink flex h-24 w-24 items-end justify-center gap-2 rounded-2xl p-5">
            {[0, 1, 2].map((i) => (
              <span key={i} className="animate-bars bg-amber block h-full w-3 rounded-[3px]" style={{ animationDelay: `${-i * 0.3}s` }} />
            ))}
          </span>
          <span className="mt-3 text-center text-sm font-semibold">{labels.job}</span>
        </div>
      </div>
      <p className="sr-only">{summary}</p>
    </div>
  );
}
