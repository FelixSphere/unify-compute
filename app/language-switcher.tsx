'use client';

import { Globe } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { LOCALE_COOKIE } from '@/lib/i18n/config';

export type LanguageLink = { code: string; prefix: string; label: string; href: string; current: boolean };

// The link already points at the same page in the other language; the cookie
// only remembers the choice. Plain <a>, not next/link: a language change is a
// full page load, so <html lang dir> always comes from the written file.
function LangLink({ link, onPick }: { link: LanguageLink; onPick?: () => void }) {
  return (
    <a
      href={link.href}
      hrefLang={link.code}
      lang={link.code}
      aria-current={link.current ? 'true' : undefined}
      onClick={() => {
        try {
          document.cookie = `${LOCALE_COOKIE}=${link.code}; path=/; max-age=31536000; samesite=lax`;
        } catch {
          // Cookies blocked: the URL still carries the language.
        }
        onPick?.();
      }}
    >
      {link.label}
    </a>
  );
}

// Header, wide screens: a menu button with the eleven labels in a popover. The
// button shows the current language's code, not its label: "Português
// (Brasil)" beside five translated nav links overran the 1240px header.
export function LanguageMenu({ links, label }: { links: LanguageLink[]; label: string }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const current = links.find((l) => l.current);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && e.target instanceof Node && !ref.current.contains(e.target)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false);
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  return (
    <div className="lang-menu" ref={ref}>
      <button
        type="button"
        className="lang-button"
        aria-label={`${label}: ${current?.label ?? ''}`}
        aria-expanded={open}
        aria-controls="lang-list"
        onClick={() => setOpen((v) => !v)}
      >
        <Globe size={16} aria-hidden="true" />
        <span className="lang-code">{(current?.prefix || 'en').toUpperCase()}</span>
      </button>
      <ul id="lang-list" className="lang-list" hidden={!open}>
        {links.map((l) => (
          <li key={l.code}>
            <LangLink link={l} onPick={() => setOpen(false)} />
          </li>
        ))}
      </ul>
    </div>
  );
}

// Inside the mobile menu and in the footer: the eleven labels as a plain list.
export function LanguageList({ links, label, className }: { links: LanguageLink[]; label: string; className?: string }) {
  return (
    <nav aria-label={label} className={className}>
      <ul>
        {links.map((l) => (
          <li key={l.code}>
            <LangLink link={l} />
          </li>
        ))}
      </ul>
    </nav>
  );
}
