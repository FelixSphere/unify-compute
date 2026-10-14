import Link from 'next/link';
import { ArrowRight } from 'lucide-react';
import { localizeHref, type Locale } from '@/lib/i18n/config';
import { rich } from '@/lib/i18n/rich';
import { getT } from '@/lib/i18n/translate';
import { gpuLabel, listings, PUBLISHER, runtimeLabel, summary } from '../catalogue';
import { SiteFooter } from '../site-footer';
import { SiteHeader } from '../site-header';
import { pageMetadata } from '../shell';

export function marketplaceMetadata(locale: Locale) {
  const t = getT(locale);
  return pageMetadata(
    locale,
    '/marketplace',
    t('Marketplace — Unify Compute'),
    t(
      'Reviewed job templates for batch inference, embeddings and fine-tuning. Pick one, point it at your data, and run it on Unify Compute GPUs.',
    ),
  );
}

export function MarketplacePage({ locale }: { locale: Locale }) {
  const t = getT(locale);
  return (
    <main id="top">
      <a className="skip-link" href="#listings">
        {t('Skip to content')}
      </a>
      <SiteHeader locale={locale} path="/marketplace" current="/marketplace" />
      <section className="market wrap">
        <div className="market-head">
          <div>
            <p className="eyebrow">{t('Marketplace')}</p>
            <h1>{t('Templates, ready to run')}</h1>
          </div>
          <p>
            {t(
              'Each template is a reviewed image, command and GPU under one name. Point it at your data and submit it with your key. You pay for the GPU time the job uses, nothing more.',
            )}
          </p>
        </div>
        <p className="market-count">{t('{count} templates · published by {publisher}', { count: listings.length, publisher: PUBLISHER })}</p>
        <ul className="market-grid" id="listings">
          {listings.map((l) => (
            <li key={l.id}>
              <Link className="listing-card" href={localizeHref(`/marketplace/${l.id}`, locale)}>
                <div className="listing-top">
                  <code>{l.id}</code>
                  <span className={l.runnable ? 'badge' : 'badge muted'}>{l.runnable ? t('Available') : t('Not yet')}</span>
                </div>
                <h2>{t(l.title)}</h2>
                <p>{t(summary(l.description))}</p>
                <dl className="listing-facts">
                  <div>
                    <dt>GPU</dt>
                    <dd dir="ltr">{gpuLabel(l.gpu)}</dd>
                  </div>
                  <div>
                    <dt>{t('Default runtime')}</dt>
                    <dd>{runtimeLabel(l.max_runtime_seconds)}</dd>
                  </div>
                </dl>
                <span className="listing-more">
                  {t('View template')} <ArrowRight size={16} className="dir-icon" />
                </span>
              </Link>
            </li>
          ))}
        </ul>
        <div className="early-access" role="note">
          <span className="badge">{t('Early access')}</span>
          <p>
            {rich(
              t(
                'Running a template needs an account with GPU job execution enabled. It is turned on per account while it rolls out; the <a>quick start</a> says how to ask.',
              ),
              { a: (text) => <Link href={localizeHref('/start', locale)}>{text}</Link> },
            )}
          </p>
        </div>
      </section>
      <SiteFooter locale={locale} path="/marketplace" current="/marketplace" />
    </main>
  );
}
