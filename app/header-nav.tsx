'use client';

import Image from 'next/image';
import Link from 'next/link';
import { Menu, X } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { BrandLockup } from './brand';
import { AccountLink } from './account-link';
import { LanguageList, LanguageMenu, type LanguageLink } from './language-switcher';
import { LOG_IN_URL, SIGN_UP_URL } from './links';

export type HeaderLabels = {
  home: string;
  nav: string;
  marketplace: string;
  quickStart: string;
  logIn: string;
  signUp: string;
  openMenu: string;
  closeMenu: string;
  language: string;
};

type NavItem = { href: string; label: string };

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

export function HeaderNav({
  homeHref,
  current,
  anchors,
  marketplaceHref,
  startHref,
  labels,
  languages,
}: {
  homeHref: string;
  current?: '/start' | '/marketplace';
  anchors: NavItem[];
  marketplaceHref: string;
  startHref: string;
  labels: HeaderLabels;
  languages: LanguageLink[];
}) {
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
    // The panel only exists below 1360px; widening the window must not leave it open and invisible.
    const wide = window.matchMedia('(min-width: 1360px)');
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
        <Link href={homeHref} className="brand" aria-label={labels.home}>
          <BrandLockup decorative eager />
          {/* 1474×1461 artwork: at 42px wide the mark itself is 32px tall. */}
          <Image unoptimized loading="eager" src="/brand/unifyai-mark-black.svg" alt="" width={42} height={41.6} className="brand-mark" />
        </Link>
        <span className="product-divider" aria-hidden="true" />
        {/* Partner descriptor, like Unify Data's "Data & Realset": same type, one string. Names, so never translated. */}
        <span className="product-name" dir="ltr">
          Compute <span className="product-partner">&amp; Flatkey</span>
        </span>
      </div>
      <nav aria-label={labels.nav} className="nav-inline">
        {anchors.map((item) => (
          <a key={item.href} href={item.href} aria-current={isCurrent(item.href) ? 'location' : undefined}>
            {item.label}
          </a>
        ))}
        <Link href={marketplaceHref} aria-current={current === '/marketplace' ? 'page' : undefined}>
          {labels.marketplace}
        </Link>
        <Link href={startHref} aria-current={current === '/start' ? 'page' : undefined}>
          {labels.quickStart}
        </Link>
      </nav>
      <div className="nav-actions">
        <LanguageMenu links={languages} label={labels.language} />
        <AccountLink href={LOG_IN_URL} className="nav-login">
          {labels.logIn}
        </AccountLink>
        <AccountLink href={SIGN_UP_URL} className="button primary">
          {labels.signUp}
        </AccountLink>
      </div>
      <button
        ref={buttonRef}
        type="button"
        className="menu-button"
        aria-label={open ? labels.closeMenu : labels.openMenu}
        aria-expanded={open}
        aria-controls="site-menu"
        onClick={() => setOpen((v) => !v)}
      >
        {open ? <X size={24} /> : <Menu size={24} />}
      </button>
      <div id="site-menu" ref={panelRef} className="menu-panel" hidden={!open} onBlur={onPanelBlur}>
        <nav aria-label={labels.nav}>
          {anchors.map((item) => (
            <a
              key={item.href}
              href={item.href}
              aria-current={isCurrent(item.href) ? 'location' : undefined}
              onClick={() => close(false)}
            >
              {item.label}
            </a>
          ))}
          <Link href={marketplaceHref} aria-current={current === '/marketplace' ? 'page' : undefined} onClick={() => close(false)}>
            {labels.marketplace}
          </Link>
          <Link href={startHref} aria-current={current === '/start' ? 'page' : undefined} onClick={() => close(false)}>
            {labels.quickStart}
          </Link>
          <AccountLink href={LOG_IN_URL} className="menu-account" onClick={() => close(false)}>
            {labels.logIn}
          </AccountLink>
          <AccountLink href={SIGN_UP_URL} className="menu-account menu-signup button primary" onClick={() => close(false)}>
            {labels.signUp}
          </AccountLink>
        </nav>
        <LanguageList links={languages} label={labels.language} className="menu-languages" />
      </div>
    </header>
  );
}
