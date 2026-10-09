/**
 * The product mark: an index card with a tab and two rules. One filled path
 * with the rules cut out (even-odd), so it stays one ink mass in the
 * surrounding colour at any size.
 */
export function Mark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 16 16"
      fill="currentColor"
      fillRule="evenodd"
      aria-hidden
      focusable="false"
      className={className}
    >
      <path d="M1 2h5.6v1.6H15V14H1V2Zm3 5.2h8v1.2H4V7.2Zm0 3h5.6v1.2H4v-1.2Z" />
    </svg>
  );
}
