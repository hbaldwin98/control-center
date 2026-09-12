/**
 * The audible half of an alert.
 *
 * A notification that arrives while you are looking at something else is only useful
 * if it makes a noise, so the shell rings a short two-tone chime whenever a
 * notification becomes available. It is synthesized rather than loaded: a few
 * oscillator nodes cost nothing, ship no binary asset, and cannot fail to download.
 *
 * Browsers refuse to start audio until the page has been interacted with, so the
 * context is created lazily and resumed on every ring. Until the first click
 * anywhere in the page, ringing is a silent no-op — which is why Settings offers a
 * Test button: it is both the check and the gesture that unlocks the sound.
 */

const MUTED_KEY = "cc.alertSound.muted";

/** Whether the ping is currently silenced. Defaults to on (not muted). */
export function isMuted(): boolean {
  try {
    return localStorage.getItem(MUTED_KEY) === "1";
  } catch {
    // A blocked or unavailable store is not a reason to lose the alert.
    return false;
  }
}

export function setMuted(muted: boolean): void {
  try {
    localStorage.setItem(MUTED_KEY, muted ? "1" : "0");
  } catch {
    // Preference is a convenience; failing to persist it must not throw into a click.
  }
}

type Ctor = typeof AudioContext;

function audioCtor(): Ctor | null {
  const w = window as unknown as { AudioContext?: Ctor; webkitAudioContext?: Ctor };
  return w.AudioContext ?? w.webkitAudioContext ?? null;
}

let ctx: AudioContext | null = null;

function context(): AudioContext | null {
  if (ctx) return ctx;
  const Ctor = audioCtor();
  if (!Ctor) return null;
  try {
    ctx = new Ctor();
  } catch {
    return null;
  }
  return ctx;
}

/** The two notes of the chime, as (frequency in Hz, delay in seconds). */
const NOTES: readonly [number, number][] = [
  [880, 0],
  [1318.5, 0.11],
];

const NOTE_SECONDS = 0.16;
const PEAK_GAIN = 0.12;

/**
 * Rings the chime, unless muted or the browser has not let audio start yet.
 *
 * `force` plays even when muted, for the Test button: pressing Test to hear nothing
 * because the sound is off would be a worse answer than the sound itself.
 */
export function ring(force = false): void {
  if (!force && isMuted()) return;
  const audio = context();
  if (!audio) return;
  // Autoplay policy parks a context created before any gesture in "suspended".
  // Resuming is a promise that rejects when the gesture has not happened; there is
  // nothing to do about that but stay quiet.
  void audio.resume?.().catch(() => {});
  if (audio.state !== "running") return;

  const start = audio.currentTime;
  for (const [freq, offset] of NOTES) {
    const osc = audio.createOscillator();
    const gain = audio.createGain();
    osc.type = "sine";
    osc.frequency.value = freq;

    // A bare start/stop clicks audibly at both ends. Ramping the gain into and out
    // of each note is what makes it read as a chime rather than a fault.
    const at = start + offset;
    gain.gain.setValueAtTime(0, at);
    gain.gain.linearRampToValueAtTime(PEAK_GAIN, at + 0.01);
    gain.gain.exponentialRampToValueAtTime(0.0001, at + NOTE_SECONDS);

    osc.connect(gain).connect(audio.destination);
    osc.start(at);
    osc.stop(at + NOTE_SECONDS);
  }
}
