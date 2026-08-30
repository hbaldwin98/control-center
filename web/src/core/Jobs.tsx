import { EmptyState, Page, PageHeader } from "@cc/ui";

/** Jobs. Filled in at milestone 4. */
export function Jobs() {
  return (
    <Page>
      <PageHeader title="Jobs" lede="The queue and its history, with per-job progress, logs, and cancellation." />
      <EmptyState>Arrives with milestone 4.</EmptyState>
    </Page>
  );
}
