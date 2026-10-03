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

// UI-STANDARD (2026-10-03): page footers carry the brand only; legal pages
// add the legal entity, FelixSphere LLC, which is not the brand.
export function CompanyLine({ legal = false }: { legal?: boolean }) {
  return (
    <span className="copyright">
      © {new Date().getFullYear()} UnifyAI
      {legal && (
        <>
          <br />
          Operated by FelixSphere LLC · 6 Karen Ct, CA 94010, United States
        </>
      )}
    </span>
  );
}
