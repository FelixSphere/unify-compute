'use client';

import Image from 'next/image';
import Link from 'next/link';
import { Menu, X } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { BrandLockup } from './brand';
import { AccountLink } from './account-link';
import { LOG_IN_URL, SIGN_UP_URL } from './links';

type NavItem = { href: string; label: string };

const NAV: NavItem[] = [
  { href: '/#workloads', label: 'Workloads' },
  { href: '/#how-it-works', label: 'How it works' },
  { href: '/#consumption', label: 'Consumption' },
];

// The links are in-page anchors, so "current" follows the URL hash.
function useCurrentHash() {
  const [hash, setHash] = useState('');
  useEffect(() => {
    const update = () => setHash(window.location.hash);
    update();
    window.addEventListener('hashchange', update);
    return () => window.removeEventListener('hashchange', update);
  }, []);
  return hash;
}

export function SiteHeader({ homeHref = '/', current }: { homeHref?: string; current?: '/start' | '/marketplace' }) {
  const [open, setOpen] = useState(false);
  const hash = useCurrentHash();
  const buttonRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);

  const close = (returnFocus: boolean) => {
    setOpen(false);
    if (returnFocus) buttonRef.current?.focus();
  };

  useEffect(() => {
    if (!open) return;
    panelRef.current?.querySelector('a')?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') close(true);
    };
    // The panel only exists below 1152px; widening the window must not leave it open and invisible.
    const wide = window.matchMedia('(min-width: 1152px)');
    const onWide = () => wide.matches && setOpen(false);
    document.addEventListener('keydown', onKey);
    wide.addEventListener('change', onWide);
    return () => {
      document.removeEventListener('keydown', onKey);
      wide.removeEventListener('change', onWide);
    };
  }, [open]);

  const onPanelBlur = (e: React.FocusEvent) => {
    const next = e.relatedTarget as Node | null;
    if (next && !panelRef.current?.contains(next) && next !== buttonRef.current) setOpen(false);
  };

  const isCurrent = (href: string) => hash !== '' && href.endsWith(hash);

  return (
    <header className="nav wrap">
      <div className="brand-group">
        <Link href={homeHref} className="brand" aria-label="Unify Compute home">
          <BrandLockup decorative eager />
          {/* 1474×1461 artwork: at 42px wide the mark itself is 32px tall. */}
          <Image unoptimized loading="eager" src="/brand/unifyai-mark-black.svg" alt="" width={42} height={41.6} className="brand-mark" />
        </Link>
        <span className="product-divider" aria-hidden="true" />
        {/* Partner descriptor, like Unify Data's "Data & Realset": same type, one string. */}
        <span className="product-name">
          Compute <span className="product-partner">&amp; Flatkey</span>
        </span>
      </div>
      <nav aria-label="Main navigation" className="nav-inline">
        {NAV.map((item) => (
          <a key={item.href} href={item.href} aria-current={isCurrent(item.href) ? 'location' : undefined}>
            {item.label}
          </a>
        ))}
        <Link href="/marketplace" aria-current={current === '/marketplace' ? 'page' : undefined}>
          Marketplace
        </Link>
        <Link href="/start" aria-current={current === '/start' ? 'page' : undefined}>
          Quick start
        </Link>
      </nav>
      <div className="nav-actions">
        <AccountLink href={LOG_IN_URL} className="nav-login">
          Log in
        </AccountLink>
        <AccountLink href={SIGN_UP_URL} className="button primary">
          Sign up
        </AccountLink>
      </div>
      <button
        ref={buttonRef}
        type="button"
        className="menu-button"
        aria-label={open ? 'Close menu' : 'Open menu'}
        aria-expanded={open}
        aria-controls="site-menu"
        onClick={() => setOpen((v) => !v)}
      >
        {open ? <X size={24} /> : <Menu size={24} />}
      </button>
      <div id="site-menu" ref={panelRef} className="menu-panel" hidden={!open} onBlur={onPanelBlur}>
        <nav aria-label="Main navigation">
          {NAV.map((item) => (
            <a
              key={item.href}
              href={item.href}
              aria-current={isCurrent(item.href) ? 'location' : undefined}
              onClick={() => close(false)}
            >
              {item.label}
            </a>
          ))}
          <Link href="/marketplace" aria-current={current === '/marketplace' ? 'page' : undefined} onClick={() => close(false)}>
            Marketplace
          </Link>
          <Link href="/start" aria-current={current === '/start' ? 'page' : undefined} onClick={() => close(false)}>
            Quick start
          </Link>
          <AccountLink href={LOG_IN_URL} className="menu-account" onClick={() => close(false)}>
            Log in
          </AccountLink>
          <AccountLink href={SIGN_UP_URL} className="menu-account menu-signup button primary" onClick={() => close(false)}>
            Sign up
          </AccountLink>
        </nav>
      </div>
    </header>
  );
}
