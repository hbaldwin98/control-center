import { EmptyState, Page, PageHeader } from "@cc/ui";

/** Dashboard. Filled in at milestone 4. */
export function Dashboard() {
  return (
    <Page>
      <PageHeader title="Dashboard" lede="Per-plugin state, spend today and this hour, running jobs, and recent alerts." />
      <EmptyState>Arrives with milestone 4.</EmptyState>
    </Page>
  );
}
