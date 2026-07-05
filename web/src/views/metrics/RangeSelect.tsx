import { For } from "solid-js";
import { RANGES } from "./catalog";

// The range control, shared by every surface that carries charts, so "Last hour"
// means the same window and the same resolution on the dashboard, in a project
// section, and on a workload page.
export default function RangeSelect(props: {
  value: string;
  onChange: (id: string) => void;
  label?: string;
}) {
  return (
    <label class="field">
      <span>{props.label ?? "Range"}</span>
      <select value={props.value} onChange={(e) => props.onChange(e.currentTarget.value)}>
        <For each={RANGES}>{(r) => <option value={r.id}>{r.label}</option>}</For>
      </select>
    </label>
  );
}
