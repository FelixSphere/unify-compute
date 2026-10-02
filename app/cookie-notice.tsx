'use client';

import Link from 'next/link';
import { useState, useSyncExternalStore } from 'react';

// Same storage keys and timings as felixsphere.com, so both sites behave identically.
const CONSENT_COOKIE = 'localConsent';
const REJECT_KEY = 'rejectTimestamp';
const REJECT_SNOOZE_MS = 12 * 60 * 60 * 1000;
const ONE_YEAR_S = 365 * 24 * 60 * 60;

type HubSpotQueue = unknown[][];
declare global {
  interface Window {
    _hsq?: HubSpotQueue;
  }
}

function hasConsentCookie() {
  return document.cookie
    .split('; ')
    .some((c) => c.startsWith(`${CONSENT_COOKIE}=`));
}

function readRejectTimestamp() {
  try {
    return localStorage.getItem(REJECT_KEY);
  } catch {
    return null;
  }
}

function shouldShow() {
  if (hasConsentCookie()) return false;
  const rejected = readRejectTimestamp();
  if (!rejected) return true;
  return Date.now() - parseInt(rejected, 10) > REJECT_SNOOZE_MS;
}

// Consent only changes through this component's own buttons, which update local state.
const subscribeNever = () => () => {};

// HubSpot reads this queue when its script loads, so pushing before or after load both work.
// doNotTrack sets __hs_do_not_track and stops tracking cookies; live chat keeps working.
function hubspot(...command: unknown[]) {
  (window._hsq = window._hsq || []).push(command);
}

export function CookieNotice() {
  // Hidden on the server (no cookies or storage there); the client snapshot decides after hydration.
  const shouldShowNotice = useSyncExternalStore(
    subscribeNever,
    shouldShow,
    () => false,
  );
  const [dismissed, setDismissed] = useState(false);

  const accept = () => {
    setDismissed(true);
    document.cookie = `${CONSENT_COOKIE}=true; max-age=${ONE_YEAR_S}; path=/`;
    try {
      localStorage.removeItem(REJECT_KEY);
    } catch {
      // Storage blocked: the cookie alone keeps the notice hidden.
    }
    hubspot('doNotTrack', { track: true });
  };

  const reject = () => {
    setDismissed(true);
    try {
      localStorage.setItem(REJECT_KEY, Date.now().toString());
    } catch {
      // Storage blocked: the notice shows again next visit, which is the safe outcome.
    }
    hubspot('doNotTrack');
  };

  if (dismissed || !shouldShowNotice) return null;

  return (
    <div
      // A div, as on felixsphere.com: a non-modal <dialog>'s UA styles fight the fixed positioning.
      // oxlint-disable-next-line jsx-a11y/prefer-tag-over-role
      role="dialog"
      aria-label="Cookie notice"
      className="fixed bottom-24 left-4 right-4 z-50 max-w-[480px] rounded-lg bg-(--u-btn-secondary-bg) p-5 text-(--u-btn-secondary-fg) sm:bottom-6 sm:left-6 sm:right-auto"
    >
      <p className="m-0 font-mono text-[11px] uppercase tracking-[0.14em] text-(--u-muted-on-dark)">
        Cookie notice
      </p>
      <p className="m-0 mt-3 text-[14px] leading-[1.55]">
        We use required, functional, and advertising cookies. Reject to keep
        only essential cookies. See our{' '}
        <Link
          href="/privacy"
          className="underline! underline-offset-2 hover:decoration-2"
        >
          Privacy Policy
        </Link>
        .
      </p>
      <div className="mt-4 flex gap-2">
        {/* Equal weight on purpose: rejecting must be as easy as accepting, and UI-STANDARD allows one primary per view (the page has it). */}
        <button
          type="button"
          onClick={reject}
          className="h-(--u-btn-height) flex-1 cursor-pointer rounded-lg border border-(--u-btn-ghost-border) bg-(--u-btn-ghost-bg) text-[15px] font-semibold text-(--u-btn-ghost-fg) hover:bg-(--u-paper)"
        >
          Reject cookies
        </button>
        <button
          type="button"
          onClick={accept}
          className="h-(--u-btn-height) flex-1 cursor-pointer rounded-lg border border-(--u-btn-ghost-border) bg-(--u-btn-ghost-bg) text-[15px] font-semibold text-(--u-btn-ghost-fg) hover:bg-(--u-paper)"
        >
          Accept all
        </button>
      </div>
    </div>
  );
}
