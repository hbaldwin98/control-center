/**
 * The shapes Hello reads.
 *
 * Pure: no React, no requests. Everything else in the plugin builds on this.
 */
export type Tick = {
  id: number;
  at: string;
  note: string;
  blobKey: string;
  aiText: string;
  eventId: number;
};

export type TicksPage = {
  note: string;
  ticks: Tick[];
};
