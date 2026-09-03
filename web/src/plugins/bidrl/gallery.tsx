/** The photo gallery on a lot page: a stage, a strip of thumbs, and a lightbox. */
import {
  useCallback,
  useEffect,
  useState,
} from "react";
import {
  Button,
  Card,
  Hint,
} from "@cc/ui";


export function LotPhotos({ urls }: { urls: string[] }) {
  const count = urls.length;
  const [index, setIndex] = useState(0);
  const [expanded, setExpanded] = useState(false);
  const current = urls[Math.min(index, Math.max(count - 1, 0))] ?? "";

  const step = useCallback(
    (delta: number) => {
      if (count < 2) return;
      setIndex((i) => (i + delta + count) % count);
    },
    [count],
  );

  useEffect(() => {
    if (count < 2) return;
    const next = new Image();
    next.src = urls[(index + 1) % count] ?? "";
    const prev = new Image();
    prev.src = urls[(index - 1 + count) % count] ?? "";
  }, [count, index, urls]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const tag = (e.target as HTMLElement | null)?.tagName;
      if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;
      if (e.key === "ArrowRight") {
        e.preventDefault();
        step(1);
      } else if (e.key === "ArrowLeft") {
        e.preventDefault();
        step(-1);
      } else if (e.key === "Escape") {
        setExpanded(false);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [step]);

  useEffect(() => {
    document.querySelector(`[data-bidrl-thumb="${index}"]`)?.scrollIntoView({
      inline: "nearest",
      block: "nearest",
    });
  }, [index]);

  useEffect(() => {
    if (!expanded) return;
    const scroller = document.querySelector(".cc-main");
    if (!(scroller instanceof HTMLElement)) return;
    const prev = scroller.style.overflowY;
    scroller.style.overflowY = "hidden";
    return () => {
      scroller.style.overflowY = prev;
    };
  }, [expanded]);

  if (count === 0) return null;

  const stage = (
    <div className="bidrl-gallery__stage">
      {count > 1 ? (
        <Button
          size="sm"
          className="bidrl-gallery__nav bidrl-gallery__nav--prev"
          onClick={() => step(-1)}
          aria-label="Previous photo"
        >
          Prev
        </Button>
      ) : null}
      <button
        type="button"
        className="bidrl-gallery__frame"
        onClick={() => setExpanded(true)}
        aria-label={`Photo ${index + 1} of ${count}. Enlarge`}
      >
        <img src={current} alt="" />
      </button>
      {count > 1 ? (
        <Button
          size="sm"
          className="bidrl-gallery__nav bidrl-gallery__nav--next"
          onClick={() => step(1)}
          aria-label="Next photo"
        >
          Next
        </Button>
      ) : null}
    </div>
  );

  return (
    <>
      <Card
        title="Photos"
        actions={<Hint>{index + 1} / {count}</Hint>}
      >
        <div className="bidrl-gallery">
          {stage}
          {count > 1 ? (
            <div className="bidrl-gallery__thumbs" role="list">
              {urls.map((src, i) => (
                <button
                  key={src}
                  type="button"
                  role="listitem"
                  data-bidrl-thumb={i}
                  className="bidrl-gallery__thumb"
                  aria-current={i === index ? "true" : undefined}
                  aria-label={`Photo ${i + 1}`}
                  onClick={() => setIndex(i)}
                >
                  <img src={src} alt="" />
                </button>
              ))}
            </div>
          ) : null}
          <Hint>
            {count > 1 ? "Arrow keys move. " : ""}
            Click the photo to enlarge.
          </Hint>
        </div>
      </Card>
      {expanded ? (
        <div
          className="bidrl-lightbox"
          role="dialog"
          aria-modal="true"
          aria-label={`Photo ${index + 1} of ${count}`}
          onClick={() => setExpanded(false)}
        >
          <div className="bidrl-lightbox__bar" onClick={(e) => e.stopPropagation()}>
            <span>{index + 1} / {count}</span>
            <Button size="sm" onClick={() => setExpanded(false)}>
              Close
            </Button>
          </div>
          {count > 1 ? (
            <Button
              size="sm"
              className="bidrl-lightbox__nav bidrl-lightbox__nav--prev"
              onClick={(e) => {
                e.stopPropagation();
                step(-1);
              }}
              aria-label="Previous photo"
            >
              Prev
            </Button>
          ) : null}
          <img
            src={current}
            alt=""
            onClick={(e) => {
              e.stopPropagation();
              if (count > 1) step(1);
            }}
          />
          {count > 1 ? (
            <Button
              size="sm"
              className="bidrl-lightbox__nav bidrl-lightbox__nav--next"
              onClick={(e) => {
                e.stopPropagation();
                step(1);
              }}
              aria-label="Next photo"
            >
              Next
            </Button>
          ) : null}
        </div>
      ) : null}
    </>
  );
}
