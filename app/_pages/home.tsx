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
import { gpus, listings, TIER_LABEL } from '../catalogue';
import { JobLifecycle } from '../visuals/job-lifecycle';
import { MarketplaceFlow } from '../visuals/marketplace-flow';
import { Metering } from '../visuals/metering';
import { Placement } from '../visuals/placement';
import { ScrollReveal } from '../visuals/scroll-reveal';
import { WorkloadScene } from '../visuals/workload-scene';

const rise = (step: number) => ({ animationDelay: `${step * 90}ms` });

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

  const inputs = [
    { icon: Layers3, label: t('Batch') },
    { icon: Binary, label: t('Embed') },
    { icon: ScanText, label: t('Extract') },
    { icon: SlidersHorizontal, label: t('Tune') },
  ];
  // The placement panel shows the first SKU on sale and the shelf the first
  // runnable templates, straight from the catalogue files.
  const soldSku = gpus.find((g) => g.available) ?? gpus[0];
  const shelf = listings.filter((l) => l.runnable).slice(0, 3).map((l) => l.id);

  return (
    <main id="top">
      <a className="skip-link" href="#workloads">{t('Skip to content')}</a>
      <SiteHeader locale={locale} path="/" homeHref="#top" />

      <section className="hero wrap">
        <div className="hero-copy">
          <div className="eyebrow animate-rise" style={rise(0)}><span className="status-dot"/> {t('THE NEXT WORKLOAD STARTS HERE')}</div>
          <h1 className="animate-rise" style={rise(1)}>{rich(t('Big workloads.\n[Zero reservations.]'))}</h1>
          <p className="hero-description animate-rise" style={rise(2)}>{t('On-demand inference and GPU compute for everything beyond chat.')}</p>
          <p className="hero-detail animate-rise" style={rise(3)}>{t('Batch processing. Embeddings. Document pipelines. Fine-tuning. Built around the work you run, with consumption-based billing.')}</p>
          <div className="hero-actions animate-rise" style={rise(4)}><AccountLink className="button primary" href={SIGN_UP_URL}>{t('Sign up')} <ArrowRight size={18} className="dir-icon"/></AccountLink><Link className="button ghost" href={localizeHref('/start', locale)}>{t('Quick start')}</Link><a className="text-link" href="#workloads">{t('Explore the workloads')} <ArrowUpRight size={17} className="dir-icon"/></a></div>
          <div className="hero-note animate-rise" style={rise(5)}><Zap size={14}/> {t('Early access · Job execution is rolling out gradually')}</div>
        </div>
        <div className="compute-panel animate-rise" style={rise(4)} aria-label={t('Conceptual workload execution diagram')}>
          <div className="panel-head"><span><span className="tiny-square"/> {t('WORKLOAD ENGINE')}</span><span className="panel-label">{t('CONCEPT PREVIEW')}</span></div>
          <div className="diagram-inputs">{inputs.map(({ icon: Icon, label }, i) => <span key={label} className="animate-input" style={{ animationDelay: `${i * 2}s` }}><Icon size={16}/> {label}</span>)}</div>
          <div className="flow-lines" aria-hidden="true">{[0, 1, 2, 3].map((i) => <span key={i}><i className="animate-drop" style={{ animationDelay: `${-i * 0.4}s` }}/></span>)}</div>
          <div className="engine"><BrandLockup tone="white"/><span className="engine-caption">{t('One compute layer.')}</span><div className="engine-bars" aria-hidden="true">{[0, 1, 2, 3, 4].map((i) => <i key={i} className="animate-bars" style={{ animationDelay: `${-i * 0.22}s` }}/>)}</div></div>
          <div className="flow-down" aria-hidden="true"/>
          <div className="gpu-grid">{['GPU 01', 'GPU 02', 'GPU 03', 'GPU 04'].map((gpu, i)=><div className="gpu" key={gpu}><span className="chip-grid" aria-hidden="true">{Array.from({length:9},(_,j)=><i key={j} className={`animate-chip-cell ${j<=i+3?'lit':''}`} style={{ animationDelay: `${i * 0.5 + j * 0.08 - 6}s` }}/>)}</span><span dir="ltr">{gpu}</span></div>)}</div>
          <div className="panel-foot"><span>{t('ALLOCATE → EXECUTE → RELEASE')}</span><span className="panel-foot-strong">{t('Built for the job.')}</span></div>
        </div>
      </section>
      <div className="principles"><div className="wrap"><span><span className="cross">+</span> {t('Bring your workload')}</span><span><span className="cross">+</span> {t('Compute on demand')}</span><span><span className="cross">+</span> {t('Pay by consumption')}</span></div></div>

      <section className="workloads wrap section" id="workloads"><div className="section-heading"><div><p className="eyebrow">{t('01 / WHAT YOU CAN BUILD')}</p><h2>{rich(t('AI has work to do.\nGive it the compute.'))}</h2></div><p>{rich(t('For the jobs behind your product.\nAnd the pipelines that keep it moving.'))}</p></div><div className="workload-grid" data-stagger>{workloads.map(({icon: Icon,number,title,text,tags}, i)=><article className="workload" key={number}><WorkloadScene index={i}/><div className="card-top"><Icon size={27} strokeWidth={1.5}/><span>{number}</span></div><h3>{title}</h3><p>{text}</p><div className="tags">{tags}</div></article>)}</div></section>

      <section className="process-section" id="how-it-works"><div className="wrap section"><div className="section-heading"><div><p className="eyebrow">{t('02 / THE COMPUTE MODEL')}</p><h2>{rich(t('From workload to output.\nWithout the capacity planning.'))}</h2></div><span className="subtle-label">{t('DESIGNED TO KEEP YOU BUILDING')}</span></div><JobLifecycle title={t('HOW A JOB RUNS')} labels={[t('Submit'), t('Check'), t('Launch'), t('Run'), t('Release')]} summary={t('A job is submitted with your key, checked against your quota and balance, launched on a GPU, run, and released when it finishes.')}/><div className="steps" data-stagger><article><span className="step-number">01</span><h3>{t('Define the work')}</h3><p>{t('Choose a model or bring your own job. Specify the data and resources it needs.')}</p></article><article><span className="step-number">02</span><h3>{t('Run on demand')}</h3><p>{t('The platform is designed to provision GPU compute for the task and release it when finished.')}</p></article><article><span className="step-number">03</span><h3>{t('Keep the output')}</h3><p>{t('Take the results into your application. Track consumption across your jobs.')}</p></article></div></div></section>

      <section className="wrap section" id="placement"><div className="section-heading"><div><p className="eyebrow">{t('03 / PLACEMENT')}</p><h2>{rich(t('You choose the GPU.\n[We choose the cloud.]'))}</h2></div><p>{t('Every GPU SKU carries an ordered list of cloud placements and one trust tier. A job launches into the first placement with capacity, and is never moved down a tier.')}</p></div><Placement sku={soldSku} labels={{ sku: t('SKU'), placements: t('Placements, in order'), gpu: 'GPU', tier: t(TIER_LABEL[soldSku.tier] ?? soldSku.tier) }} summary={t('A SKU is matched against its cloud placements in order, and the job launches on the first one with capacity.')}/></section>

      <section className="process-section" id="marketplace"><div className="wrap section"><div className="section-heading"><div><p className="eyebrow">{t('04 / MARKETPLACE')}</p><h2>{rich(t('Templates, reviewed\n[before anyone runs them.]'))}</h2></div><p>{t('A template is an image, a command and a GPU under one name. Each one is reviewed before it is listed. Pick one, point it at your data and submit it with your key.')}</p></div><MarketplaceFlow ids={shelf} labels={{ image: t('Image'), command: t('Command'), gpu: 'GPU', reviewed: t('Reviewed'), listed: t('Listed'), job: t('Your job') }} summary={t('An image, a command and a GPU become one template, the template is reviewed before it is listed, and a listed template runs as your job.')}/><p className="section-link"><Link className="text-link" href={localizeHref('/marketplace', locale)}>{t('Browse the marketplace')} <ArrowRight size={17} className="dir-icon"/></Link></p></div></section>

      <section className="wrap section" id="consumption"><div className="consumption"><div><p className="eyebrow">{t('05 / CONSUMPTION, NOT COMMITMENT')}</p><h2>{rich(t('Your work has a runtime.\n[Your bill should, too.]'))}</h2><p>{t('Our billing model is built around consumption, so you can match compute spend to actual workloads instead of reserving capacity ahead of time.')}</p><div className="pricing-note">{t('Rates and availability will be announced at launch.')}</div></div><div className="billing-card"><div className="billing-title"><Zap size={18}/> {t('THE PRINCIPLE')}</div><p>{rich(t('Run the job.\nUse the compute.\n[Pay for the usage.]'))}</p><div className="billing-bottom"><span>{t('Consumption-based')}</span><ArrowUpRight size={22} className="dir-icon"/></div></div></div><Metering labels={{ held: t('Held at submit'), metered: t('Metered while it runs'), settled: t('Settled to what it used') }} summary={t('When you submit a job, a hold is placed on your balance. GPU time is metered while the job runs; when it ends you pay for what it used and the rest of the hold is released.')}/></section>
      <SiteFooter locale={locale} path="/" homeHref="#top"/>
      <ScrollReveal/>
    </main>
  );
}
