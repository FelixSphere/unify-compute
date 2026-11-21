// A track with work riding it end to end, evenly spaced. Decorative.
// Horizontal from sm up; pass `vertical` for the stacked phone layout.
export function Conveyor({
  packets = 3,
  duration = 3,
  vertical = false,
  className = '',
}: {
  packets?: number;
  duration?: number;
  vertical?: boolean;
  className?: string;
}) {
  return (
    <div aria-hidden className={`rtl-flip relative overflow-hidden ${vertical ? 'mx-auto h-12 w-10' : 'h-10 min-w-6'} ${className}`}>
      <div className={`bg-ink/10 absolute rounded-full ${vertical ? 'inset-y-0 left-1/2 w-1.5 -translate-x-1/2' : 'inset-x-0 top-1/2 h-1.5 -translate-y-1/2'}`} />
      {Array.from({ length: packets }, (_, i) => (
        <span
          key={i}
          className={`bg-amber absolute rounded-md ${vertical ? 'animate-conveyor-y left-1/2 -ml-3 h-5 w-6' : 'animate-conveyor top-1/2 -mt-3 h-6 w-6 sm:w-8'}`}
          style={{ ['--conveyor-duration' as string]: `${duration}s`, animationDelay: `${-(i * duration) / packets}s` }}
        >
          {!vertical && <span className="bg-ink/70 absolute top-2 left-1.5 h-0.5 w-3 rounded-full sm:w-4" />}
          {!vertical && <span className="bg-ink/70 absolute top-3.5 left-1.5 h-0.5 w-2 rounded-full sm:w-2.5" />}
        </span>
      ))}
    </div>
  );
}
