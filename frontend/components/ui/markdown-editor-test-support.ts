/**
 * Stands in for `./paste-as-markdown` in tests that drive `MarkdownEditor`
 * with Crepe stubbed: `vi.mock("@/components/ui/paste-as-markdown", () =>
 * import("@/components/ui/markdown-editor-test-support"))`. The real plugin
 * pulls in Milkdown's kit, whose import under a parallel run can outlast a
 * wait for the editor to be built, and a stubbed Crepe never uses it.
 * `markdown-editor.mount.test.tsx` keeps the real import covered.
 */
export const pasteAsMarkdown = {};
