import type { ReactNode } from 'react';

// The path one job takes through compute-api: submitted with a key, checked
// against quota and balance, launched on a GPU, run, and released. The job
// steps from stage to stage and each stage lights while it is there. The
// glyphs draw in currentColor, so they turn with the stage.
const glyphs: ReactNode[] = [
  // Submit: a job spec.
  <span key="submit" className="flex w-1/2 flex-col gap-[18%]">
    <span className="h-[3px] rounded-full bg-current sm:h-1" />
    <span className="h-[3px] rounded-full bg-current sm:h-1" />
    <span className="h-[3px] w-2/3 rounded-full bg-current sm:h-1" />
  </span>,
  // Check: quota and balance pass.
  <svg key="check" viewBox="0 0 24 24" className="w-1/2" fill="none">
    <path d="M4 12.5 L10 18 L20 6" stroke="currentColor" strokeWidth="3.5" strokeLinecap="round" strokeLinejoin="round" />
  </svg>,
  // Launch: a GPU.
  <span key="gpu" className="grid w-1/2 grid-cols-3 gap-[12%]">
    {Array.from({ length: 9 }, (_, i) => (
      <span key={i} className="aspect-square rounded-[2px] bg-current" />
    ))}
  </span>,
  // Run: the GPU at work.
  <svg key="run" viewBox="0 0 24 24" className="w-1/2" fill="none">
    {[3, 9, 15].map((x, i) => (
      <rect key={x} x={x} y="4" width="5" height="16" rx="1.5" fill="currentColor" className="animate-bars" style={{ animationDelay: `${-i * 0.3}s` }} />
    ))}
  </svg>,
  // Release: outputs out, GPU handed back.
  <svg key="release" viewBox="0 0 24 24" className="rtl-flip w-1/2" fill="none">
    <rect x="3" y="9" width="12" height="12" rx="2" stroke="currentColor" strokeWidth="3" />
    <path d="M11 13 L20 4 M13 4 H20 V11" stroke="currentColor" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" />
  </svg>,
];

export function JobLifecycle({ title, labels, summary }: { title: string; labels: string[]; summary: string }) {
  return (
    <div className="bg-surface mt-14 rounded-2xl border p-6 sm:p-10">
      <p className="panel-caption">{title}</p>
      <div aria-hidden className="relative mt-8">
        {/* The track runs between the first and last stage centres. */}
        <div className="rtl-flip absolute inset-x-[10%] top-[22px] h-0 sm:top-[36px]">
          <div className="bg-ink/10 absolute inset-x-0 -top-[3px] h-1.5 rounded-full" />
          <span className="animate-job bg-amber border-ink absolute -top-[13px] -ml-[13px] h-[26px] w-[26px] rounded-md border-[3px] sm:-top-4 sm:-ml-4 sm:h-8 sm:w-8" />
        </div>
        <ol className="relative m-0 grid list-none grid-cols-5 p-0">
          {labels.map((label, i) => (
            <li key={label} className="flex min-w-0 flex-col items-center text-center">
              <span
                className="animate-stage bg-ink text-paper flex h-11 w-11 items-center justify-center rounded-xl sm:h-[72px] sm:w-[72px] sm:rounded-2xl"
                style={{ animationDelay: `${i * 2}s` }}
              >
                {glyphs[i]}
              </span>
              <span className="mt-3 w-full px-0.5 text-xs font-semibold break-words hyphens-auto sm:mt-4 sm:text-sm">{label}</span>
            </li>
          ))}
        </ol>
      </div>
      <p className="sr-only">{summary}</p>
    </div>
  );
}
