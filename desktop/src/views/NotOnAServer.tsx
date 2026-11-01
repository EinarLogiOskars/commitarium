// What a feature that needs this computer's folders shows in remote mode.
export function NotOnAServer({ what }: { what: string }) {
  return <p className="muted set__note">{what} is not available on a server yet.</p>;
}
