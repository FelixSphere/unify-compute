import { listings } from '../../../catalogue';
import { ListingPage, listingMetadata } from '../../../_pages/listing';

type Params = { params: Promise<{ id: string }> };

export const dynamicParams = false;

export function generateStaticParams() {
  return listings.map((l) => ({ id: l.id }));
}

export async function generateMetadata({ params }: Params) {
  return listingMetadata('en', (await params).id);
}

export default async function Page({ params }: Params) {
  return <ListingPage locale="en" id={(await params).id} />;
}
