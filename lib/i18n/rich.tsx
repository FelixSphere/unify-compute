import { Fragment, type ReactNode } from 'react';

type Tags = Record<string, (children: string) => ReactNode>;

// Renders one translated string that carries its own markup, so a sentence is
// never split into separately translated fragments and translators can move
// every part with the word order:
//   [text]          the emphasized phrase of a headline (<span>)
//   <tag>text</tag> an element the caller supplies, e.g. a link
//   {name}          a node the caller supplies, e.g. <code>
//   a newline       a line break
export function rich(text: string, tags: Tags = {}, nodes: Record<string, ReactNode> = {}): ReactNode {
  return text.split(/(<\w+>[^<]*<\/\w+>|\{\w+\}|\[[^\]]+\]|\n)/).map((part, i) => {
    const tag = part.match(/^<(\w+)>([^<]*)<\/\1>$/);
    if (tag && tags[tag[1]]) return <Fragment key={i}>{tags[tag[1]](tag[2])}</Fragment>;
    const node = part.match(/^\{(\w+)\}$/);
    if (node && node[1] in nodes) return <Fragment key={i}>{nodes[node[1]]}</Fragment>;
    if (part === '\n') return <br key={i} />;
    if (part.length > 2 && part.startsWith('[') && part.endsWith(']')) return <span key={i}>{part.slice(1, -1)}</span>;
    return part;
  });
}
