/** The live bid feed, and the badge that says it is connected. */
import {
  useEffect,
  useMemo,
  useState,
} from "react";
import {
  type Lot,
  type BidObserved,
  type BidOverlay,
} from "./model";


// BidRL pushes bids over its own realtime feed rather than making the gallery poll.
// The plugin joins that feed for exactly as long as this stream is open: the route
// holds it for the lifetime of the request, so navigating away closes the socket with
// no job, no window, and nothing to clean up.
//
// Nothing is rendered from this stream. The bids are written to the plugin's own rows
// and announced as bidrl events, which is what already revalidates the snapshot below,
// so there is one path for data and no second way for a screen to disagree with it.
/**
 * `off` covers every reason there is no feed -- not asked for, not connected, closed by
 * the server, or nothing on screen to watch. A screen only ever distinguishes "these
 * prices are arriving as they happen" from "these prices are a snapshot".
 */
export type LiveStatus = "off" | "live";

/** What a screen gets from the live feed: whether it is connected, and the bids that
 *  have arrived since it was. */
export type LiveBids = { status: LiveStatus; bids: BidOverlay };

const noBids: BidOverlay = {};

/**
 * Watches the lots on screen through the host's push endpoint.
 *
 * The transport belongs to control-center, not to this plugin: one authenticated
 * connection carries whatever topics were asked for, and the host refcounts them, so
 * ten screens watching the same lot cost BidRL one upstream channel. All this hook
 * does is name the topics -- one per lot -- and fold the messages that come back.
 */
export function useLiveBids(lots: readonly Lot[] | undefined, enabled = true): LiveBids {
  const key = useMemo(
    () => Array.from(new Set((lots ?? []).map((lot) => lot.id).filter(Boolean))).sort().join(","),
    [lots],
  );
  const [status, setStatus] = useState<LiveStatus>("off");
  const [bids, setBids] = useState<BidOverlay>(noBids);
  useEffect(() => {
    if (!enabled || !key || typeof EventSource === "undefined") {
      setStatus("off");
      setBids(noBids);
      return;
    }
    // A new watch set is a new set of prices: anything folded for the old one would be
    // describing rows this screen is no longer showing.
    setBids(noBids);
    const topics = key.split(",").map((id) => `lot:${id}`).join(",");
    const source = new EventSource(
      `/api/push/bidrl?topics=${encodeURIComponent(topics)}`,
      { withCredentials: true },
    );
    // Topics not yet known to be fed. Every topic starts dark: an open connection
    // only means the host accepted it, and the plugin behind it may still be dialling
    // or already broken. The host announces the ones it is feeding when the connection
    // opens, so a reconnect resyncs rather than sitting dark until the next change.
    //
    // The badge is live only while the connection is up and nothing is dark, because
    // "some of these prices are live" is not something one badge can honestly say.
    let connected = false;
    const dark = new Set(topics.split(","));
    const settle = () => setStatus(connected && dark.size === 0 ? "live" : "off");
    const off = () => {
      connected = false;
      settle();
    };
    // "ready" is the host's own frame. EventSource fires a native "open" of its own,
    // which arrives first and means only that the response started, so the badge waits
    // for the frame that says the topics are registered.
    source.addEventListener("ready", () => {
      connected = true;
      settle();
    });
    // A payload is itself proof the topic is being fed, whatever was said before.
    source.addEventListener("message", (event) => {
      const topic = topicOf(event);
      if (topic) dark.delete(topic);
      settle();
    });
    source.addEventListener("message", (event) => {
      const bid = bidFromFrame(event);
      if (bid) setBids((current) => ({ ...current, [bid.lotId]: bid }));
    });
    // The plugin could not feed a lot, or its feed came back. Either way the screen
    // keeps working off its snapshot, which is why this only moves a badge.
    source.addEventListener("unavailable", (event) => {
      const topic = topicOf(event);
      if (topic) dark.add(topic);
      settle();
    });
    source.addEventListener("available", (event) => {
      const topic = topicOf(event);
      if (topic) dark.delete(topic);
      settle();
    });
    source.addEventListener("closed", off);
    // Do NOT close here. EventSource reconnects on its own, and closing on the first
    // error turns any transient drop into a permanent one -- the stream delivers a
    // message or two and is then gone for good. readyState 2 means the browser has
    // given up; anything else means it is still retrying, and the badge should come
    // back by itself when it succeeds.
    source.onerror = () => {
      off();
      if (source.readyState === 2) source.close();
    };
    return () => {
      off();
      source.close();
    };
  }, [key, enabled]);
  return { status, bids };
}

/** Reads the topic a control frame is about. */
function topicOf(event: MessageEvent): string {
  try {
    return (JSON.parse(event.data) as { topic?: string }).topic ?? "";
  } catch {
    return "";
  }
}

/** Reads one bid out of a push frame. The host frames every message the same way --
 *  a topic and the plugin's own payload -- so this is the only place that shape is
 *  known. */
function bidFromFrame(event: MessageEvent): BidObserved | null {
  try {
    const frame = JSON.parse(event.data) as { topic?: string; data?: BidObserved };
    const bid = frame.data;
    return bid && typeof bid.lotId === "string" && bid.currentBidCents !== undefined ? bid : null;
  } catch {
    return null;
  }
}

/** Says whether the prices on screen are arriving as they happen. Absent when they are
 *  not, because a permanently visible "not live" is just noise. */
export function LiveDot({ status }: { status: LiveStatus }) {
  if (status !== "live") return null;
  return (
    <span className="bidrl-live" title="Bids are updating as they are placed">
      <span className="bidrl-live-dot" aria-hidden="true" />
      Live
    </span>
  );
}
