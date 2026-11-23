import { cache } from "react";

import { bestEffort } from "@/lib/api/page-read";

import { fetchIdentity } from "./session";

/**
 * Who is signed in, for what only decorates or picks a default: null when
 * nobody is, and also when the API could not say. Once per render, so an
 * outage is one log line however many parts of the screen ask. A screen that
 * decides who may go where reads `fetchIdentity`, since "unknown" must not
 * read as "signed out".
 */
export const readIdentity = cache(() => bestEffort(fetchIdentity, "/auth/me"));
