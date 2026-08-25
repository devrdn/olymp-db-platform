import "@testing-library/jest-dom/vitest";

import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// Testing Library only auto-cleans when Vitest globals are enabled. They are
// not, so the previous render would otherwise stay in the document and the
// next query would find two of everything.
afterEach(cleanup);
