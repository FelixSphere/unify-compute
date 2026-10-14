import { HomePage, homeMetadata } from '../_pages/home';

export const metadata = homeMetadata('en');

export default function Page() {
  return <HomePage locale="en" />;
}
