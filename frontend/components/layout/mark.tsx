/**
 * The product mark: an index card with a raised tab and two ruled lines.
 *
 * The direction is called Kartoteka, and the desk card index is the reason —
 * a drawer of ruled cards is the direct ancestor of a row in a relational
 * table, and that kinship is what the whole visual language is built on. The
 * mark says it in sixteen pixels rather than in a paragraph.
 *
 * Drawn as one filled path with the rules cut out by the even-odd rule, so it
 * stays a single ink mass at any size and takes the surrounding colour.
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
