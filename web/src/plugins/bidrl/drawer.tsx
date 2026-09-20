/**
 * The lot drawer: a lot opened over whatever list you are on, so the list keeps its
 * filters, its loaded pages, and its scroll. The open lot lives in the URL (`?lot=<id>`),
 * which is what makes the browser's back button close it and what makes the open lot
 * shareable.
 */
import { useRef, type ReactNode } from "react";
import { Drawer, useNavigate, useQueryState } from "@cc/ui";
import { LotDrawerContext } from "./lotparts";
import { LotDetail, useLotDetail } from "./lotdetail";

export function LotDrawerHost({ children }: { children: ReactNode }) {
  const [id, setLot] = useQueryState("lot");
  const navigate = useNavigate();
  // Opening from a list pushes one history entry, so Back closes the drawer instead of
  // leaving the list. Moving between lots after that replaces that same entry.
  const pushed = useRef(false);

  const open = (next: string) => {
    if (id) {
      setLot(next, { history: "replace" });
    } else {
      setLot(next, { history: "push" });
      pushed.current = true;
    }
  };

  const close = () => {
    if (pushed.current) {
      pushed.current = false;
      navigate(-1);
    } else {
      setLot("");
    }
  };

  return (
    <LotDrawerContext.Provider value={{ id, open, close }}>
      {children}
      {id ? <LotDrawerPanel id={id} onClose={close} /> : null}
    </LotDrawerContext.Provider>
  );
}

function LotDrawerPanel({ id, onClose }: { id: string; onClose: () => void }) {
  const detail = useLotDetail(id);
  return (
    <Drawer open onClose={onClose} label="Lot details" closeLabel="Close lot">
      <LotDetail detail={detail} chrome={false} />
    </Drawer>
  );
}
