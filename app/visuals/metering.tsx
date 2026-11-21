// How a job is paid for (compute-api ledger, ADR-0008): a hold is placed on
// the balance at submit, GPU time is metered in ticks while the job runs,
// and the job settles to what it used, releasing the rest of the hold. No
// amounts and no durations: the proportions are illustrative.
const TICKS = 12;

export function Metering({ labels, summary }: { labels: { held: string; metered: string; settled: string }; summary: string }) {
  return (
    <div className="bg-surface mt-14 rounded-2xl border p-6 sm:p-10">
      <div aria-hidden className="rtl-flip bg-ink/5 relative h-14 overflow-hidden rounded-xl sm:h-16">
        <div className="animate-hold border-amber bg-amber-tint absolute inset-0 rounded-xl border-[3px] border-dashed" />
        <div className="absolute inset-y-[10px] left-[10px] w-[calc(62%-10px)]">
          <div className="animate-meter bg-ink absolute inset-0 rounded-md" />
          {Array.from({ length: TICKS - 1 }, (_, i) => (
            <span key={i} className="bg-surface absolute inset-y-0 w-[3px]" style={{ left: `${((i + 1) * 100) / TICKS}%` }} />
          ))}
        </div>
      </div>
      <ul aria-hidden className="m-0 mt-6 flex list-none flex-wrap gap-x-8 gap-y-3 p-0 text-sm">
        <li className="flex items-center gap-3">
          <span className="border-amber bg-amber-tint h-5 w-8 shrink-0 rounded-md border-[3px] border-dashed" />
          {labels.held}
        </li>
        <li className="flex items-center gap-3">
          <span className="bg-ink h-5 w-8 shrink-0 rounded-md" />
          {labels.metered}
        </li>
        <li className="flex items-center gap-3">
          <span className="bg-amber h-5 w-8 shrink-0 rounded-md" />
          {labels.settled}
        </li>
      </ul>
      <p className="sr-only">{summary}</p>
    </div>
  );
}
