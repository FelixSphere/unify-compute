// A looping scene at the top of each workload card, in the order the cards
// are listed: batch, embeddings, document pipelines, fine-tuning. Shapes
// only: no text, no figures, so the scene can never say more than the card.
export function WorkloadScene({ index }: { index: number }) {
  return (
    <div aria-hidden className="bg-paper mb-6 overflow-hidden rounded-xl border">
      <svg viewBox="0 0 320 112" className="rtl-flip block h-28 w-full" fill="none">
        {index === 0 && <Batch />}
        {index === 1 && <Embeddings />}
        {index === 2 && <Documents />}
        {index === 3 && <FineTuning />}
      </svg>
    </div>
  );
}

// Records stream into one GPU and come out the other side done.
function Batch() {
  const rows = [22, 40, 58, 76, 94];
  return (
    <>
      {rows.map((y, i) => (
        <rect key={y} x="18" y={y - 5} width="58" height="10" rx="3" className="fill-ink/80 animate-blink" style={{ animationDelay: `${-i * 0.36}s` }} />
      ))}
      <path d="M84 56 H124" className="animate-flow stroke-ink" strokeWidth="3" />
      {[0, 1, 2].map((i) => (
        <rect key={i} x="82" y="50" width="16" height="12" rx="3" className="animate-travel fill-amber" style={{ ['--travel' as string]: '34px', animationDelay: `${-i * 0.6}s` }} />
      ))}
      <rect x="128" y="22" width="68" height="68" rx="10" className="fill-ink" />
      <rect x="140" y="34" width="44" height="44" rx="5" className="fill-amber" />
      {[0, 1, 2].map((c) =>
        [0, 1, 2].map((r) => <rect key={`${c}${r}`} x={146 + c * 12} y={40 + r * 12} width="8" height="8" rx="1.5" className="fill-ink/70" />),
      )}
      <path d="M200 56 H238" className="animate-flow stroke-ink" strokeWidth="3" />
      {[0, 1, 2].map((i) => (
        <rect key={i} x="200" y="50" width="16" height="12" rx="3" className="animate-travel fill-amber" style={{ ['--travel' as string]: '32px', animationDelay: `${-i * 0.6 - 0.3}s` }} />
      ))}
      {rows.map((y, i) => (
        <rect key={y} x="246" y={y - 5} width="58" height="10" rx="3" className="animate-grow-x fill-amber" style={{ animationDelay: `${i * 0.18}s` }} />
      ))}
    </>
  );
}

// Pages become points, and the points settle into clusters.
const clusterA = [
  [212, 30], [228, 24], [222, 42], [240, 36], [206, 48],
];
const clusterB = [
  [262, 72], [278, 66], [270, 86], [290, 80], [256, 90],
];
function Embeddings() {
  return (
    <>
      {[0, 1, 2].map((i) => (
        <g key={i} transform={`translate(${18 + i * 10} ${20 + i * 8})`}>
          <rect width="48" height="62" rx="5" className="fill-surface stroke-ink" strokeWidth="3" />
          <path d="M10 16 h28 M10 26 h28 M10 36 h18" className="stroke-ink/60" strokeWidth="3" strokeLinecap="round" />
        </g>
      ))}
      <path d="M100 56 C 140 56 150 40 196 40" className="animate-flow stroke-ink" strokeWidth="3" />
      <path d="M100 56 C 140 56 200 80 246 80" className="animate-flow stroke-ink" strokeWidth="3" />
      <path d="M196 102 H306 M196 102 V12" className="stroke-ink/25" strokeWidth="2" />
      {clusterA.map(([x, y], i) => (
        <circle key={`a${i}`} cx={x} cy={y} r="7" className="animate-pop fill-ink" style={{ animationDelay: `${i * 0.12}s` }} />
      ))}
      {clusterB.map(([x, y], i) => (
        <circle key={`b${i}`} cx={x} cy={y} r="7" className="animate-pop fill-amber" style={{ animationDelay: `${0.6 + i * 0.12}s` }} />
      ))}
    </>
  );
}

// A reader passes down the page; fields are written out on the right.
function Documents() {
  return (
    <>
      <rect x="24" y="12" width="76" height="90" rx="6" className="fill-surface stroke-ink" strokeWidth="3" />
      {[28, 40, 52, 64, 76, 88].map((y, i) => (
        <path key={y} d={`M36 ${y} h${i % 3 === 2 ? 34 : 52}`} className="stroke-ink/55" strokeWidth="3.5" strokeLinecap="round" />
      ))}
      <rect x="18" y="18" width="88" height="5" rx="2.5" className="animate-scan fill-amber" style={{ ['--scan' as string]: '76px' }} />
      <path d="M110 56 H160" className="animate-flow stroke-ink" strokeWidth="3" />
      {[0, 1].map((i) => (
        <rect key={i} x="108" y="50" width="16" height="12" rx="3" className="animate-travel fill-amber" style={{ ['--travel' as string]: '40px', animationDelay: `${-i * 0.9}s` }} />
      ))}
      {[22, 48, 74].map((y, i) => (
        <g key={y}>
          <rect x="168" y={y} width="30" height="18" rx="4" className="fill-ink" />
          <rect x="204" y={y} width={98 - i * 18} height="18" rx="4" className="animate-grow-x fill-amber" style={{ animationDelay: `${i * 0.3}s` }} />
        </g>
      ))}
    </>
  );
}

// Your data and a base model go in; an adapter grows on top as the loss
// curve falls.
function FineTuning() {
  return (
    <>
      {[0, 1, 2, 3].map((i) => (
        <rect key={i} x="18" y={20 + i * 20} width="40" height="14" rx="3" className="fill-amber animate-blink" style={{ animationDelay: `${-i * 0.45}s` }} />
      ))}
      <path d="M64 56 H100" className="animate-flow stroke-ink" strokeWidth="3" />
      <rect x="104" y="46" width="72" height="52" rx="8" className="fill-ink" />
      <path d="M118 62 h44 M118 72 h44 M118 82 h30" className="stroke-paper/60" strokeWidth="3" strokeLinecap="round" />
      <rect x="104" y="18" width="72" height="22" rx="6" className="animate-grow-x fill-amber" style={{ animationDuration: '4.2s' }} />
      <path d="M196 102 H306 M196 102 V14" className="stroke-ink/25" strokeWidth="2" />
      <path
        d="M204 22 C 222 60 236 74 256 82 S 290 92 302 94"
        pathLength={1}
        className="animate-draw stroke-ink"
        strokeWidth="4"
        strokeLinecap="round"
        style={{ animationDuration: '4.2s' }}
      />
    </>
  );
}
