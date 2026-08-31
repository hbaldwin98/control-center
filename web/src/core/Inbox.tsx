import { useCallback } from "react";
import { Link } from "react-router-dom";
import {
  Async,
  Badge,
  Button,
  Card,
  Page,
  PageHeader,
  RelativeTime,
  Stack,
  Table,
  api,
  useSnapshot,
} from "@cc/ui";

type InboxPage = {
  notifications: InboxItem[];
  nextAfter: string;
};

type InboxItem = {
  id: string;
  sourceEventId: number;
  title: string;
  body: string;
  url: string;
  subject: string;
  collapsed: number;
  read: boolean;
  createdAt: string;
  availableAt: string;
  ruleId: string;
};

/** Inbox of rule-matched events. Lifecycle events refetch this snapshot. */
export function Inbox() {
  const page = useSnapshot<InboxPage>(
    useCallback((signal) => api.snapshot<InboxPage>("/api/notifications?limit=100", { signal }), []),
    { events: "core.notification.**" },
  );

  return (
    <Page>
      <PageHeader
        title="Inbox"
        lede="Committed events that matched a notification rule. Mark read; the row stays."
      />
      <Async state={page} loading="Loading inbox…" empty="Nothing in the inbox yet." isEmpty={(d) => d.notifications.length === 0}>
        {(data) => (
          <Card>
            <Table
              head={
                <>
                  <th>When</th>
                  <th>Title</th>
                  <th>Subject</th>
                  <th></th>
                </>
              }
            >
              {data.notifications.map((n) => (
                <tr key={n.id}>
                  <td>
                    <RelativeTime at={n.createdAt} />
                  </td>
                  <td>
                    {n.read ? n.title : <strong>{n.title}</strong>}
                    {n.collapsed > 0 ? (
                      <>
                        {" "}
                        <Badge tone="warn">+{n.collapsed}</Badge>
                      </>
                    ) : null}
                    {n.body ? <div className="cc-hint">{n.body}</div> : null}
                  </td>
                  <td>{n.subject || n.ruleId}</td>
                  <td>
                    <Stack>
                      {n.url ? <Link to={n.url}>Open</Link> : null}
                      <Button
                        size="sm"
                        onClick={() => {
                          void api.post(`/api/notifications/${encodeURIComponent(n.id)}/read`, { read: !n.read }).then(() => page.reload());
                        }}
                      >
                        {n.read ? "Mark unread" : "Mark read"}
                      </Button>
                    </Stack>
                  </td>
                </tr>
              ))}
            </Table>
          </Card>
        )}
      </Async>
    </Page>
  );
}
