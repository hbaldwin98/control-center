/** The full lot page, for a lot opened directly or reloaded on its own URL. */
import { Page, PageHeader, Stack, useRouteParams } from "@cc/ui";
import { useLotDetail, LotDetail } from "../lotdetail";
import { LiveDot } from "../live";
import { BidrlTabs } from "../chrome";

export function LotView() {
  const id = useRouteParams().id ?? "";
  const detail = useLotDetail(id);
  const { live, lot } = detail;
  return (
    <Page>
      <PageHeader
        eyebrow="BidRL / Lots"
        title={lot?.title ?? "Lot"}
        actions={<LiveDot status={live.status} />}
      />
      <Stack>
        <BidrlTabs />
        <LotDetail detail={detail} />
      </Stack>
    </Page>
  );
}
