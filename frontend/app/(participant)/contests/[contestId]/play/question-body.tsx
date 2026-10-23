import { StoryText } from "@/components/product/story-text";

/**
 * A question's wording as its participant reads it, through `StoryText`, where
 * the security boundary lives. `page.tsx` renders it on the server for every
 * question the page loads with; `QuestionsPanel` loads it on demand for one
 * that opens later, so the Markdown parser reaches the browser only then.
 */
export function QuestionBody({ markdown }: { markdown: string }) {
  return <StoryText markdown={markdown} className="max-w-none font-sans text-body" />;
}
