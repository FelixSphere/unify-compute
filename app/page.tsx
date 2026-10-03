import Link from 'next/link';
import { BrandLockup, CompanyLine } from './brand';
import { SiteHeader } from './site-header';
import { ArrowUpRight, ArrowRight, Layers3, ScanText, SlidersHorizontal, Binary, Zap } from 'lucide-react';

const workloads = [
  { icon: Layers3, number: '01', title: 'Batch processing', text: 'From a thousand records to your entire dataset. Run inference in bulk, on your schedule.', tags: 'DATA ENRICHMENT / OFFLINE INFERENCE' },
  { icon: Binary, number: '02', title: 'Embeddings', text: 'Turn unstructured data into useful vectors. Build the foundation for search and retrieval.', tags: 'SEMANTIC SEARCH / RAG' },
  { icon: ScanText, number: '03', title: 'Document pipelines', text: 'Extract, classify, and transform documents into structured data your applications can use.', tags: 'EXTRACTION / CLASSIFICATION' },
  { icon: SlidersHorizontal, number: '04', title: 'Fine-tuning', text: 'Bring your data. Adapt models to your domain with GPU compute that fits the job.', tags: 'MODEL ADAPTATION / TRAINING' },
];

export default function Home() {
  return (
    <main id="top">
      <a className="skip-link" href="#workloads">Skip to content</a>
      <SiteHeader homeHref="#top" />

      <section className="hero wrap">
        <div className="hero-copy">
          <div className="eyebrow"><span className="status-dot"/> THE NEXT WORKLOAD STARTS HERE</div>
          <h1>Big workloads.<br/>Zero reservations.</h1>
          <p className="hero-description">On-demand inference and GPU compute for everything beyond chat.</p>
          <p className="hero-detail">Batch processing. Embeddings. Document pipelines. Fine-tuning. Built around the work you run, with consumption-based billing.</p>
          <div className="hero-actions"><a className="button primary" href="#workloads">Explore the workloads <ArrowRight size={18}/></a><a className="text-link" href="#how-it-works">Meet Unify Compute <ArrowUpRight size={17}/></a></div>
          <div className="hero-note"><Zap size={14}/> In development · A new compute platform from UnifyAI</div>
        </div>
        <div className="compute-panel" aria-label="Conceptual workload execution diagram">
          <div className="panel-head"><span><span className="tiny-square"/> WORKLOAD ENGINE</span><span className="panel-label">CONCEPT PREVIEW</span></div>
          <div className="diagram-inputs"><span><Layers3 size={16}/> Batch</span><span><Binary size={16}/> Embed</span><span><ScanText size={16}/> Extract</span><span><SlidersHorizontal size={16}/> Tune</span></div>
          <div className="flow-lines" aria-hidden="true"><span/><span/><span/><span/></div>
          <div className="engine"><BrandLockup tone="white"/><span className="engine-caption">One compute layer.</span><div className="engine-bars" aria-hidden="true"><i/><i/><i/><i/><i/></div></div>
          <div className="flow-down" aria-hidden="true"/>
          <div className="gpu-grid">{['GPU 01', 'GPU 02', 'GPU 03', 'GPU 04'].map((gpu, i)=><div className="gpu" key={gpu}><span className="chip-grid" aria-hidden="true">{Array.from({length:9},(_,j)=><i key={j} className={j<=i+3?'lit':''}/>)}</span><span>{gpu}</span></div>)}</div>
          <div className="panel-foot"><span>ALLOCATE → EXECUTE → RELEASE</span><span className="panel-foot-strong">Built for the job.</span></div>
        </div>
      </section>
      <div className="principles"><div className="wrap"><span><span className="cross">+</span> Bring your workload</span><span><span className="cross">+</span> Compute on demand</span><span><span className="cross">+</span> Pay by consumption</span></div></div>

      <section className="workloads wrap section" id="workloads"><div className="section-heading"><div><p className="eyebrow">01 / WHAT YOU CAN BUILD</p><h2>AI has work to do.<br/>Give it the compute.</h2></div><p>For the jobs behind your product.<br/>And the pipelines that keep it moving.</p></div><div className="workload-grid">{workloads.map(({icon: Icon,number,title,text,tags})=><article className="workload" key={number}><div className="card-top"><Icon size={27} strokeWidth={1.5}/><span>{number}</span></div><h3>{title}</h3><p>{text}</p><div className="tags">{tags}</div></article>)}</div></section>

      <section className="process-section" id="how-it-works"><div className="wrap section"><div className="section-heading"><div><p className="eyebrow">02 / THE COMPUTE MODEL</p><h2>From workload to output.<br/>Without the capacity planning.</h2></div><span className="subtle-label">DESIGNED TO KEEP YOU BUILDING</span></div><div className="steps"><article><span className="step-number">01</span><h3>Define the work</h3><p>Choose a model or bring your own job. Specify the data and resources it needs.</p></article><article><span className="step-number">02</span><h3>Run on demand</h3><p>The platform is designed to provision GPU compute for the task and release it when finished.</p></article><article><span className="step-number">03</span><h3>Keep the output</h3><p>Take the results into your application. Track consumption across your jobs.</p></article></div></div></section>

      <section className="consumption wrap section" id="consumption"><div><p className="eyebrow">03 / CONSUMPTION, NOT COMMITMENT</p><h2>Your work has a runtime.<br/><span>Your bill should, too.</span></h2><p>Our billing model is built around consumption, so you can match compute spend to actual workloads instead of reserving capacity ahead of time.</p><div className="pricing-note">Rates and availability will be announced at launch.</div></div><div className="billing-card"><div className="billing-title"><Zap size={18}/> THE PRINCIPLE</div><p>Run the job.<br/>Use the compute.<br/><span>Pay for the usage.</span></p><div className="billing-bottom"><span>Consumption-based</span><ArrowUpRight size={22}/></div></div></section>
      <footer className="wrap"><a href="#top" className="brand" aria-label="Unify Compute home"><BrandLockup decorative/></a><span>Compute beyond chat.</span><Link href="/privacy" className="footer-privacy">Privacy</Link><CompanyLine/></footer>
    </main>
  );
}
