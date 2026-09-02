import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  resolve: { tsconfigPaths: true },
  test: {
    environment: "jsdom",
    setupFiles: ["./vitest.setup.ts"],
    // `proxy.ts` is named separately because Next requires it at the root, and
    // the convention here is that a test sits beside its source. It was
    // outside the glob while the proxy was sending every signed-out visitor
    // into a redirect loop, and an untested file is how that stayed unnoticed.
    include: ["{app,lib,components}/**/*.test.{ts,tsx}", "proxy.test.ts"],
  },
});
