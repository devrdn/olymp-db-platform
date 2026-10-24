import { StoryText } from "@/components/product/story-text";

/**
 * An organiser's answer or announcement, read through `StoryText`, where the
 * security boundary lives: React elements, never an HTML string. Organisers
 * write the story in the same Markdown, and an answer often names a table or
 * quotes a query. The questions tab loads this on demand, so the Markdown
 * parser reaches the browser only once there is something to render.
 */
export function ClarificationText({ markdown }: { markdown: string }) {
  return <StoryText markdown={markdown} className="max-w-none font-sans text-body" />;
}
