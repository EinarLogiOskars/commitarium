/** A label/value line, as used by the settings sheet. */
export function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="set__fact">
      <span className="muted">{label}</span>
      <span className="set__fact-value">{value}</span>
    </div>
  );
}
