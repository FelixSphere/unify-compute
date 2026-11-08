import Link from 'next/link';
import { ArrowRight } from 'lucide-react';
import { localizeHref, type Locale } from '@/lib/i18n/config';
import { rich } from '@/lib/i18n/rich';
import { getT } from '@/lib/i18n/translate';
import { gpuLabel, gpus, listings, PUBLISHER, runtimeLabel, summary, TIER_LABEL } from '../catalogue';
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
        <div className="gpu-section" id="gpus">
          <div className="section-heading">
            <div>
              <p className="eyebrow">GPUs</p>
              <h2>{t('Choose the GPU for the job')}</h2>
            </div>
            <span className="subtle-label">{t('PICK ONE IN THE RUN FORM, OR PASS ITS ID AS SKU')}</span>
          </div>
          <table className="gpu-table">
            <thead>
              <tr>
                <th scope="col">GPU</th>
                <th scope="col">{t('Memory')}</th>
                <th scope="col">vCPU · RAM</th>
                <th scope="col">{t('Tier')}</th>
                <th scope="col">{t('Billing')}</th>
                <th scope="col">{t('Status')}</th>
              </tr>
            </thead>
            <tbody>
              {gpus.map((g) => (
                <tr key={g.id}>
                  <td dir="ltr">
                    <strong>{g.gpu_count > 1 ? `${g.gpu_count} × ` : ''}{g.gpu}</strong>
                    <code>{g.id}</code>
                  </td>
                  <td dir="ltr">{g.gpu_memory_gb} GB</td>
                  <td dir="ltr">{g.vcpus} · {g.memory_gb} GB</td>
                  <td>{t(TIER_LABEL[g.tier] ?? g.tier)}</td>
                  <td>{g.interruptible ? t('Spot') : t('On-demand')}</td>
                  <td><span className={g.available ? 'badge' : 'badge muted'}>{g.available ? t('Available') : t('Coming soon')}</span></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
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
