/**
 * The shell's scrolling element. Screens scroll inside it, not on the document, so
 * anything that reads or restores scroll position goes through here instead of reaching
 * for the shell's markup by class name.
 */
export function mainScrollElement(): HTMLElement | null {
  const el = document.querySelector(".cc-main");
  return el instanceof HTMLElement ? el : null;
}
