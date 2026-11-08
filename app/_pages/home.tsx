import Link from 'next/link';
import { ArrowUpRight, ArrowRight, Layers3, ScanText, SlidersHorizontal, Binary, Zap } from 'lucide-react';
import { localizeHref, type Locale } from '@/lib/i18n/config';
import { rich } from '@/lib/i18n/rich';
import { getT } from '@/lib/i18n/translate';
import { BrandLockup } from '../brand';
import { AccountLink } from '../account-link';
import { SIGN_UP_URL } from '../links';
import { SiteFooter } from '../site-footer';
import { SiteHeader } from '../site-header';
import { pageMetadata } from '../shell';

export function homeMetadata(locale: Locale) {
  const t = getT(locale);
  return pageMetadata(
    locale,
    '/',
    t('Unify Compute — On-demand inference & GPU compute'),
    t(
      'Compute beyond chat. On-demand GPU compute for batch processing, embeddings, document pipelines, and fine-tuning, with consumption-based billing.',
    ),
  );
}

export function HomePage({ locale }: { locale: Locale }) {
  const t = getT(locale);
  const workloads = [
    { icon: Layers3, number: '01', title: t('Batch processing'), text: t('From a thousand records to your entire dataset. Run inference in bulk, on your schedule.'), tags: t('DATA ENRICHMENT / OFFLINE INFERENCE') },
    { icon: Binary, number: '02', title: t('Embeddings'), text: t('Turn unstructured data into useful vectors. Build the foundation for search and retrieval.'), tags: t('SEMANTIC SEARCH / RAG') },
    { icon: ScanText, number: '03', title: t('Document pipelines'), text: t('Extract, classify, and transform documents into structured data your applications can use.'), tags: t('EXTRACTION / CLASSIFICATION') },
    { icon: SlidersHorizontal, number: '04', title: t('Fine-tuning'), text: t('Bring your data. Adapt models to your domain with GPU compute that fits the job.'), tags: t('MODEL ADAPTATION / TRAINING') },
  ];

  return (
    <main id="top">
      <a className="skip-link" href="#workloads">{t('Skip to content')}</a>
      <SiteHeader locale={locale} path="/" homeHref="#top" />

      <section className="hero wrap">
        <div className="hero-copy">
          <div className="eyebrow"><span className="status-dot"/> {t('THE NEXT WORKLOAD STARTS HERE')}</div>
          <h1>{rich(t('Big workloads.\nZero reservations.'))}</h1>
          <p className="hero-description">{t('On-demand inference and GPU compute for everything beyond chat.')}</p>
          <p className="hero-detail">{t('Batch processing. Embeddings. Document pipelines. Fine-tuning. Built around the work you run, with consumption-based billing.')}</p>
          <div className="hero-actions"><AccountLink className="button primary" href={SIGN_UP_URL}>{t('Sign up')} <ArrowRight size={18} className="dir-icon"/></AccountLink><Link className="button ghost" href={localizeHref('/start', locale)}>{t('Quick start')}</Link><a className="text-link" href="#workloads">{t('Explore the workloads')} <ArrowUpRight size={17} className="dir-icon"/></a></div>
          <div className="hero-note"><Zap size={14}/> {t('Early access · Job execution is rolling out gradually')}</div>
        </div>
        <div className="compute-panel" aria-label={t('Conceptual workload execution diagram')}>
          <div className="panel-head"><span><span className="tiny-square"/> {t('WORKLOAD ENGINE')}</span><span className="panel-label">{t('CONCEPT PREVIEW')}</span></div>
          <div className="diagram-inputs"><span><Layers3 size={16}/> {t('Batch')}</span><span><Binary size={16}/> {t('Embed')}</span><span><ScanText size={16}/> {t('Extract')}</span><span><SlidersHorizontal size={16}/> {t('Tune')}</span></div>
          <div className="flow-lines" aria-hidden="true"><span/><span/><span/><span/></div>
          <div className="engine"><BrandLockup tone="white"/><span className="engine-caption">{t('One compute layer.')}</span><div className="engine-bars" aria-hidden="true"><i/><i/><i/><i/><i/></div></div>
          <div className="flow-down" aria-hidden="true"/>
          <div className="gpu-grid">{['GPU 01', 'GPU 02', 'GPU 03', 'GPU 04'].map((gpu, i)=><div className="gpu" key={gpu}><span className="chip-grid" aria-hidden="true">{Array.from({length:9},(_,j)=><i key={j} className={j<=i+3?'lit':''}/>)}</span><span dir="ltr">{gpu}</span></div>)}</div>
          <div className="panel-foot"><span>{t('ALLOCATE → EXECUTE → RELEASE')}</span><span className="panel-foot-strong">{t('Built for the job.')}</span></div>
        </div>
      </section>
      <div className="principles"><div className="wrap"><span><span className="cross">+</span> {t('Bring your workload')}</span><span><span className="cross">+</span> {t('Compute on demand')}</span><span><span className="cross">+</span> {t('Pay by consumption')}</span></div></div>

      <section className="workloads wrap section" id="workloads"><div className="section-heading"><div><p className="eyebrow">{t('01 / WHAT YOU CAN BUILD')}</p><h2>{rich(t('AI has work to do.\nGive it the compute.'))}</h2></div><p>{rich(t('For the jobs behind your product.\nAnd the pipelines that keep it moving.'))}</p></div><div className="workload-grid">{workloads.map(({icon: Icon,number,title,text,tags})=><article className="workload" key={number}><div className="card-top"><Icon size={27} strokeWidth={1.5}/><span>{number}</span></div><h3>{title}</h3><p>{text}</p><div className="tags">{tags}</div></article>)}</div></section>

      <section className="process-section" id="how-it-works"><div className="wrap section"><div className="section-heading"><div><p className="eyebrow">{t('02 / THE COMPUTE MODEL')}</p><h2>{rich(t('From workload to output.\nWithout the capacity planning.'))}</h2></div><span className="subtle-label">{t('DESIGNED TO KEEP YOU BUILDING')}</span></div><div className="steps"><article><span className="step-number">01</span><h3>{t('Define the work')}</h3><p>{t('Choose a model or bring your own job. Specify the data and resources it needs.')}</p></article><article><span className="step-number">02</span><h3>{t('Run on demand')}</h3><p>{t('The platform is designed to provision GPU compute for the task and release it when finished.')}</p></article><article><span className="step-number">03</span><h3>{t('Keep the output')}</h3><p>{t('Take the results into your application. Track consumption across your jobs.')}</p></article></div></div></section>

      <section className="consumption wrap section" id="consumption"><div><p className="eyebrow">{t('03 / CONSUMPTION, NOT COMMITMENT')}</p><h2>{rich(t('Your work has a runtime.\n[Your bill should, too.]'))}</h2><p>{t('Our billing model is built around consumption, so you can match compute spend to actual workloads instead of reserving capacity ahead of time.')}</p><div className="pricing-note">{t('Rates and availability will be announced at launch.')}</div></div><div className="billing-card"><div className="billing-title"><Zap size={18}/> {t('THE PRINCIPLE')}</div><p>{rich(t('Run the job.\nUse the compute.\n[Pay for the usage.]'))}</p><div className="billing-bottom"><span>{t('Consumption-based')}</span><ArrowUpRight size={22} className="dir-icon"/></div></div></section>
      <SiteFooter locale={locale} path="/" homeHref="#top"/>
    </main>
  );
}
