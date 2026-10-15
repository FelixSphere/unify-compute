import type { Metadata } from 'next';
import Link from 'next/link';
import { ArrowRight } from 'lucide-react';
import { SiteFooter } from '../site-footer';
import { SiteHeader } from '../site-header';
import { gpuLabel, gpus, listings, PUBLISHER, runtimeLabel, summary, TIER_LABEL } from './catalogue';

export const metadata: Metadata = {
  title: 'Marketplace — Unify Compute',
  description:
    'Reviewed job templates for batch inference, embeddings and fine-tuning. Pick one, point it at your data, and run it on Unify Compute GPUs.',
};

export default function Marketplace() {
  return (
    <main id="top">
      <a className="skip-link" href="#listings">
        Skip to content
      </a>
      <SiteHeader current="/marketplace" />
      <section className="market wrap">
        <div className="market-head">
          <div>
            <p className="eyebrow">Marketplace</p>
            <h1>Templates, ready to run</h1>
          </div>
          <p>
            Each template is a reviewed image, command and GPU under one name.
            Point it at your data and submit it with your key. You pay for the
            GPU time the job uses, nothing more.
          </p>
        </div>
        <p className="market-count">
          {listings.length} templates · published by {PUBLISHER}
        </p>
        <ul className="market-grid" id="listings">
          {listings.map((l) => (
            <li key={l.id}>
              <Link className="listing-card" href={`/marketplace/${l.id}`}>
                <div className="listing-top">
                  <code>{l.id}</code>
                  <span className={l.runnable ? 'badge' : 'badge muted'}>{l.runnable ? 'Available' : 'Not yet'}</span>
                </div>
                <h2>{l.title}</h2>
                <p>{summary(l.description)}</p>
                <dl className="listing-facts">
                  <div>
                    <dt>GPU</dt>
                    <dd>{gpuLabel(l.gpu)}</dd>
                  </div>
                  <div>
                    <dt>Default runtime</dt>
                    <dd>{runtimeLabel(l.max_runtime_seconds)}</dd>
                  </div>
                </dl>
                <span className="listing-more">
                  View template <ArrowRight size={16} />
                </span>
              </Link>
            </li>
          ))}
        </ul>
        <div className="gpu-section" id="gpus">
          <div className="section-heading">
            <div>
              <p className="eyebrow">GPUs</p>
              <h2>Choose the GPU for the job</h2>
            </div>
            <span className="subtle-label">PICK ONE IN THE RUN FORM, OR PASS ITS ID AS SKU</span>
          </div>
          <table className="gpu-table">
            <thead>
              <tr>
                <th scope="col">GPU</th>
                <th scope="col">Memory</th>
                <th scope="col">vCPU · RAM</th>
                <th scope="col">Tier</th>
                <th scope="col">Billing</th>
                <th scope="col">Status</th>
              </tr>
            </thead>
            <tbody>
              {gpus.map((g) => (
                <tr key={g.id}>
                  <td>
                    <strong>{g.gpu_count > 1 ? `${g.gpu_count} × ` : ''}{g.gpu}</strong>
                    <code>{g.id}</code>
                  </td>
                  <td>{g.gpu_memory_gb} GB</td>
                  <td>{g.vcpus} · {g.memory_gb} GB</td>
                  <td>{TIER_LABEL[g.tier] ?? g.tier}</td>
                  <td>{g.interruptible ? 'Spot' : 'On-demand'}</td>
                  <td><span className={g.available ? 'badge' : 'badge muted'}>{g.available ? 'Available' : 'Coming soon'}</span></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="early-access" role="note">
          <span className="badge">Early access</span>
          <p>
            Running a template needs an account with GPU job execution enabled.
            It is turned on per account while it rolls out; the{' '}
            <Link href="/start">quick start</Link> says how to ask.
          </p>
        </div>
      </section>
      <SiteFooter current="/marketplace" />
    </main>
  );
}
