/** Column sorting, shared by every table in the plugin. */
import {
  useState,
} from "react";
import {
  SortHeader,
} from "@cc/ui";
import {
  cycleSort,
  type SortDir,
  type SortState,
} from "./model";


export function useColumnSort<C extends string>(defaults: Record<C, SortDir>) {
  const [sort, setSort] = useState<SortState<C> | null>(null);
  const onSort = (column: C) => setSort((cur) => cycleSort(cur, column, defaults[column]));
  return { sort, onSort };
}

export function SortedHead<C extends string>({
  column,
  sort,
  onSort,
  numeric,
  children,
}: {
  column: C;
  sort: SortState<C> | null;
  onSort: (column: C) => void;
  numeric?: boolean;
  children: string;
}) {
  return (
    <SortHeader
      active={sort?.column === column}
      direction={sort?.dir ?? "asc"}
      numeric={numeric}
      onClick={() => onSort(column)}
    >
      {children}
    </SortHeader>
  );
}
