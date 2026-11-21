'use client';

import { useEffect } from 'react';

// Sections below the fold rise into place as they are scrolled to, and the
// direct children of any [data-stagger] container inside them follow one by
// one. The hidden state is applied here, after hydration, and only to
// sections not yet on screen: without JavaScript, with reduced motion, or for
// anything already visible at load, the page is simply static.
export function ScrollReveal() {
  useEffect(() => {
    if (typeof IntersectionObserver === 'undefined') return;
    if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return;

    const below = (el: Element) => el.getBoundingClientRect().top > window.innerHeight;
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (!entry.isIntersecting) continue;
          entry.target.classList.add('revealed');
          observer.unobserve(entry.target);
        }
      },
      { rootMargin: '0px 0px -10% 0px' },
    );

    for (const section of document.querySelectorAll<HTMLElement>('main > section')) {
      const sectionBelow = below(section);
      section.querySelectorAll<HTMLElement>('[data-stagger]').forEach((container) => {
        [...container.children].forEach((child, i) => (child as HTMLElement).style.setProperty('--i', String(i)));
        // A section already on screen at load stays put, but its card groups
        // further down still come in as they are reached.
        if (!sectionBelow && below(container)) {
          container.classList.add('reveal-items');
          observer.observe(container);
        }
      });
      if (sectionBelow) {
        section.classList.add('reveal');
        observer.observe(section);
      }
    }

    return () => observer.disconnect();
  }, []);

  return null;
}
