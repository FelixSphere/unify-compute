// Ported from FelixSphere/unifyapi app/__tests__/reduced-motion.test.ts, on
// node:test so the site needs no test framework. Every animation globals.css
// defines must be stopped (or set to its end state) under
// prefers-reduced-motion: reduce, and every animate-* class a component uses
// must exist. The failure mode is invisible to anyone who does not set the
// preference, which is everyone writing the animation; and a misspelled
// utility class is silently inert.
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

const appDir = join(process.cwd(), 'app');
const css = readFileSync(join(appDir, 'globals.css'), 'utf8');

// Tailwind ships these; they are not ours to define, and AGENTS.md's motion
// rule says not to use them.
const TAILWIND_BUILTINS = new Set(['spin', 'ping', 'pulse', 'bounce', 'none']);

function reducedMotionBlock(source) {
  const at = source.indexOf('@media (prefers-reduced-motion: reduce)');
  if (at === -1) return '';
  const open = source.indexOf('{', at);
  let depth = 0;
  for (let i = open; i < source.length; i += 1) {
    if (source[i] === '{') depth += 1;
    else if (source[i] === '}') {
      depth -= 1;
      if (depth === 0) return source.slice(open + 1, i);
    }
  }
  return '';
}

const names = (source, pattern) => new Set([...source.matchAll(pattern)].map((m) => m[1]));

function tsxFiles(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return tsxFiles(path);
    return entry.name.endsWith('.tsx') ? [path] : [];
  });
}

const block = reducedMotionBlock(css);
const defined = names(css.replace(block, ''), /\.animate-([a-z0-9-]+)\s*(?:,|\{)/g);
const disabled = names(block, /\.animate-([a-z0-9-]+)/g);

describe('reduced motion', () => {
  it('has a prefers-reduced-motion block at all', () => {
    assert.notEqual(block.trim(), '');
  });

  it('there is only one, so the check above reads all of it', () => {
    assert.equal(css.split('@media (prefers-reduced-motion: reduce)').length, 2);
  });

  it('disables every animation globals.css defines', () => {
    assert.ok(defined.size > 0);
    for (const name of defined) {
      assert.ok(disabled.has(name), `.animate-${name} is never disabled under prefers-reduced-motion`);
    }
  });

  it('defines every animation the components actually use, and none of Tailwind’s', () => {
    const used = new Set();
    for (const file of tsxFiles(appDir)) {
      for (const m of readFileSync(file, 'utf8').matchAll(/\banimate-([a-z0-9-]+)\b/g)) {
        assert.ok(!TAILWIND_BUILTINS.has(m[1]) || m[1] === 'none', `${file} uses Tailwind's animate-${m[1]}`);
        used.add(m[1]);
      }
    }
    assert.ok(used.size > 0);
    for (const name of used) {
      assert.ok(defined.has(name), `animate-${name} is used but never defined in globals.css`);
    }
  });

  it('hides the scroll-reveal state unless motion is welcome', () => {
    const before = css.slice(0, css.indexOf('@media (prefers-reduced-motion: no-preference)'));
    assert.ok(!/\.reveal\s*\{/.test(before), '.reveal must be defined only inside the no-preference block');
    assert.ok(css.includes('@media (prefers-reduced-motion: no-preference)'));
  });
});
