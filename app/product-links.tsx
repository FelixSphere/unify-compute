import { ArrowUpRight } from 'lucide-react';
import type { Locale } from '@/lib/i18n/config';
import { rich } from '@/lib/i18n/rich';
import { getT } from '@/lib/i18n/translate';
import { PRODUCTS, productHref } from './products';

// Plain <a href> in the same tab with no parameters, as the standard asks:
// these are links between our own sites, and crawlers should follow them.

export function FooterProducts({ locale }: { locale: Locale }) {
  const t = getT(locale);
  return (
    <nav aria-labelledby="footer-products-title" className="footer-products">
      <h2 id="footer-products-title">{t('UnifyAI products')}</h2>
      <ul>
        {PRODUCTS.map((p) => (
          <li key={p.name}>
            {p.current ? (
              <span className="product-entry" aria-current="page">
                <strong>{p.name}</strong> <em>{t('This site')}</em>
                <span>{p.line(t)}</span>
              </span>
            ) : (
              <a className="product-entry" href={productHref(p.url, locale)}>
                <strong>{p.name}</strong>
                <span>{p.line(t)}</span>
              </a>
            )}
          </li>
        ))}
      </ul>
    </nav>
  );
}

export function ProductsSection({ locale }: { locale: Locale }) {
  const t = getT(locale);
  return (
    <section className="process-section" id="products">
      <div className="wrap section">
        <div className="section-heading">
          <div>
            <p className="eyebrow">{t('06 / ALL PRODUCTS')}</p>
            <h2>{rich(t('Six products.\n[One UnifyAI.]'))}</h2>
          </div>
          <p>{t('Unify Compute is one of six UnifyAI products. Each one has its own site.')}</p>
        </div>
        <ul className="product-grid" data-stagger>
          {PRODUCTS.map((p) => {
            const body = (
              <>
                <div className="product-card-top">
                  <h3>{p.name}</h3>
                  {p.current ? <span className="badge">{t('This site')}</span> : <ArrowUpRight size={18} className="dir-icon" aria-hidden />}
                </div>
                <p>{p.line(t)}</p>
              </>
            );
            return (
              <li key={p.name}>
                {p.current ? (
                  <div className="product-card" aria-current="page">
                    {body}
                  </div>
                ) : (
                  <a className="product-card" href={productHref(p.url, locale)}>
                    {body}
                  </a>
                )}
              </li>
            );
          })}
        </ul>
      </div>
    </section>
  );
}
