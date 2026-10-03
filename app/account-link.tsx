import Link from 'next/link';

// The account URLs are placeholders that may be on this site or on another
// host; same-site ones go through the router so they navigate client-side.
export function AccountLink({ href, children, ...rest }: React.ComponentProps<'a'> & { href: string }) {
  return href.startsWith('/') ? (
    <Link href={href} {...rest}>
      {children}
    </Link>
  ) : (
    <a href={href} {...rest}>
      {children}
    </a>
  );
}
