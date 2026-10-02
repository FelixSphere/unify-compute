import Image from 'next/image';

// The wordmark is fixed artwork: always this SVG, never text in a web font.
// 132px keeps it above the 120px lockup minimum; clear space is CSS (.brand).
export function BrandLockup({ tone = 'black' }: { tone?: 'black' | 'white' }) {
  return (
    <Image
      unoptimized
      src={`/brand/unifyai-lockup-horizontal-${tone}.svg`}
      alt="UnifyAI"
      width={132}
      height={45}
      className="brand-lockup"
    />
  );
}
