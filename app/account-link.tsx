import Link from 'next/link';

// Account URLs may be on this site or on the app host; same-site ones go
// through the router so they navigate client-side.
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
