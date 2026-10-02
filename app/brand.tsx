import Image from 'next/image';

// The wordmark is fixed artwork: always this SVG, never text in a web font.
// At 148px the mark inside it is 34px tall (UI-STANDARD header size); the
// SVG carries 8.5px of its own padding, and .brand adds the rest of the
// clear space, which is the mark's own height.
// decorative: the enclosing link already carries the accessible name.
export function BrandLockup({ tone = 'black', decorative = false, eager = false }: { tone?: 'black' | 'white'; decorative?: boolean; eager?: boolean }) {
  return (
    <Image
      unoptimized
      loading={eager ? 'eager' : 'lazy'}
      src={`/brand/unifyai-lockup-horizontal-${tone}.svg`}
      alt={decorative ? '' : 'UnifyAI'}
      width={148}
      height={50.9}
      className="brand-lockup"
    />
  );
}

export function CompanyLine() {
  return (
    <span className="copyright">
      © {new Date().getFullYear()} UnifyAI · Lebuh Bandar Utama PJU 6, 47800 Petaling Jaya, Selangor, Malaysia
    </span>
  );
}
