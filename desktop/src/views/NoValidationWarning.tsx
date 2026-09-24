// Shown wherever a merge may happen without any isolated validation configured.
// The project is then trusting the two agents' approval alone; this makes that
// explicit and offers a one-click jump to set validation up.
export function NoValidationWarning({
  message,
  onOpenValidation,
}: {
  message: string;
  onOpenValidation: () => void;
}) {
  return (
    <div className="banner banner--warn no-validation">
      <span>{message}</span>
      <button className="ghost no-validation__action" onClick={onOpenValidation}>
        Set up validation
      </button>
    </div>
  );
}
