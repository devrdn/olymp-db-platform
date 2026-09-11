"use client";

import { useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { Combobox, type ComboboxOption } from "@/components/ui/combobox";
import { ApiError, request } from "@/lib/api/client";
import { MIN_DIRECTORY_QUERY_LENGTH } from "@/lib/api/people-terms";
import { debounce, type Debounced } from "@/lib/format/debounce";

/**
 * How long to wait after the last keystroke before asking the directory.
 *
 * The same window `app/(admin)/users/filters.tsx` already settled on, and for
 * the same reason (see its own comment): under roughly 200ms a burst of
 * keystrokes is not collapsed into one request, and past roughly 500ms the
 * results feel detached from the typing. That window is set by a person's
 * typing rhythm, not by which field is being searched, so the number carries
 * over unchanged to a lighter endpoint over a shorter string.
 */
const SEARCH_PAUSE_MS = 300;

/** What the directory returns: enough to tell two candidates apart, submit
 * the right one, and — since the owner decided the trade in
 * backend/internal/api/contest_people_handler.go's PersonResponse is worth
 * it — an email for when the login and the name alone still leave two
 * "Ivanov"s standing. Empty for an account that never set one; see
 * `describeCandidate` below for how that empty value is kept from showing up
 * as a stray separator in the popup. */
export type Candidate = { userId: string; login: string; fullName: string; email: string };

function isRawCandidate(
  value: unknown,
): value is { user_id: string; login: string; full_name: string; email?: string } {
  if (!value || typeof value !== "object") return false;
  const v = value as Record<string, unknown>;
  return (
    typeof v.user_id === "string" &&
    typeof v.login === "string" &&
    typeof v.full_name === "string" &&
    (v.email === undefined || typeof v.email === "string")
  );
}

/**
 * Parsed by hand rather than through `lib/api/people`'s zod schemas: this file
 * is a client component, and a schema module calls `z.object()` at load time,
 * which would ship zod's parser to every browser that opens this screen for
 * four fields it already trusts its own backend to shape correctly.
 */
function parseDirectory(payload: unknown): Candidate[] {
  if (!payload || typeof payload !== "object") return [];
  const items = (payload as Record<string, unknown>).items;
  if (!Array.isArray(items)) return [];
  return items
    .filter(isRawCandidate)
    .map((raw) => ({
      userId: raw.user_id,
      login: raw.login,
      fullName: raw.full_name,
      // PersonResponse omits the key entirely for an account with none
      // (`omitempty`, so it never has to publish `"email": ""`) — read back
      // here as the same empty string the rest of this file treats as
      // "nothing to show", rather than as `undefined` needing its own check
      // everywhere the value is used.
      email: raw.email ?? "",
    }));
}

/** The popup's second line: a login always tells two accounts apart on its
 * own, and an email joins it now that the owner has accepted the trade of
 * showing one — but only when the account has one, so an account with none
 * shows a bare login rather than a separator pointing at nothing. */
function describeCandidate(candidate: Candidate): string {
  return candidate.email ? `${candidate.login} · ${candidate.email}` : candidate.login;
}

function toOption(candidate: Candidate): ComboboxOption<Candidate> {
  return { key: candidate.userId, value: candidate, label: candidate.fullName, description: describeCandidate(candidate) };
}

/**
 * Search by login, name or email; choose one; submit their id.
 *
 * One control behind both the staff form and the one-person participant
 * form — see people-panels.tsx — because both are the same act: find a
 * person, then act on the account they turn out to be. The bulk roster
 * import stays a separate textarea (actions.ts's `importParticipantsAction`);
 * this is for adding one person, not a spreadsheet.
 */
export function PersonPicker({
  id,
  name,
  contestId,
  label,
  placeholder,
  helpText,
  searchingText,
  noResultsText,
  searchFailedText,
  changeText,
  selectedTemplate,
  disabled,
  help,
  helpLabel,
}: {
  id: string;
  /** The hidden field's name — what the surrounding `<form>` submits. */
  name: string;
  contestId: string;
  label: string;
  placeholder: string;
  helpText: string;
  searchingText: string;
  noResultsText: string;
  searchFailedText: string;
  changeText: string;
  /** "Selected: {name} ({login})", filled in once somebody is chosen. */
  selectedTemplate: string;
  disabled?: boolean;
  /** Passed through to `Combobox`: an explanation behind a "?" beside the label. */
  help?: string;
  helpLabel?: string;
}) {
  const [inputValue, setInputValue] = useState("");
  const [options, setOptions] = useState<Candidate[]>([]);
  const [chosen, setChosen] = useState<ComboboxOption<Candidate> | null>(null);
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState(false);
  // Guards against an earlier, slower request overwriting a later, faster
  // one — the out-of-order reply a plain debounce does not by itself
  // prevent, since the debounce only spaces out when requests start, not
  // when they return.
  const generation = useRef(0);
  // Holds the debounced runner itself. Built inside an effect rather than
  // `useMemo`, and called only from event handlers below: both are where refs
  // may be read, render itself is not (see the react-hooks/refs rule this
  // file used to trip — `generation.current`, read from inside a closure a
  // `useMemo` factory returned, looked like a render-time read even though it
  // only ever ran later, off a timer).
  const runSearchRef = useRef<Debounced<[string]> | null>(null);

  useEffect(() => {
    const debounced = debounce((query: string) => {
      const trimmed = query.trim();
      // Mirrors the server's own floor (MIN_DIRECTORY_QUERY_LENGTH, from
      // contests.MinDirectoryQueryLength): below it the directory endpoint
      // always answers with an empty list, so asking is a round trip spent on
      // an answer already known. An empty box is covered by the same check —
      // it is shorter than the minimum too.
      if (trimmed.length < MIN_DIRECTORY_QUERY_LENGTH) {
        generation.current += 1;
        setOptions([]);
        setLoading(false);
        setFailed(false);
        return;
      }

      const thisGeneration = ++generation.current;
      setLoading(true);
      setFailed(false);

      request(`/contests/${contestId}/people/directory?q=${encodeURIComponent(trimmed)}`)
        .then((payload) => {
          if (thisGeneration !== generation.current) return;
          setOptions(parseDirectory(payload));
        })
        .catch((error: unknown) => {
          if (thisGeneration !== generation.current) return;
          setOptions([]);
          setFailed(true);
          // failed/searchFailedText already tells the person the search did
          // not work; an error that is not even an ApiError (a network
          // failure, a bug in parseDirectory) is unexpected on top of that,
          // and worth a place a developer can actually see it. Rethrowing
          // here would not do that — nothing downstream of this .catch
          // handles a rethrow, so it would only become an unhandled
          // rejection, visible to nobody in particular.
          if (!(error instanceof ApiError)) console.error("directory search failed", error);
        })
        .finally(() => {
          if (thisGeneration === generation.current) setLoading(false);
        });
    }, SEARCH_PAUSE_MS);

    runSearchRef.current = debounced;
    // A timer that fires after this control is gone (unmounted, or about to
    // rebuild for a new contestId) would set state nobody can see any more.
    return () => {
      debounced.cancel();
      runSearchRef.current = null;
    };
  }, [contestId]);

  function runSearch(query: string) {
    runSearchRef.current?.(query);
  }

  const status = loading ? searchingText : failed ? searchFailedText : "";

  return (
    <div className="flex flex-col gap-2">
      <Combobox
        id={id}
        label={label}
        items={options.map(toOption)}
        inputValue={inputValue}
        onInputValueChange={(next) => {
          setInputValue(next);
          // Editing the text after a choice was made means the choice no
          // longer describes what is in the box; holding onto it would let a
          // change of mind submit an id nobody can see chosen any more.
          if (chosen && next !== chosen.label) setChosen(null);
          runSearch(next);
        }}
        value={chosen}
        onValueChange={(next) => {
          setChosen(next);
          if (next) runSearchRef.current?.cancel();
        }}
        placeholder={placeholder}
        // Blank below the minimum a search actually runs at, or while one is
        // in flight: "no matches" is only true once an answer has actually
        // come back for what is currently in the box, and nothing shorter
        // than MIN_DIRECTORY_QUERY_LENGTH ever asked.
        emptyMessage={
          loading || inputValue.trim().length < MIN_DIRECTORY_QUERY_LENGTH ? "" : noResultsText
        }
        statusMessage={status}
        disabled={disabled}
        describedBy={`${id}-help`}
        help={help}
        helpLabel={helpLabel}
      />

      {/* `role="status"` + `aria-live="polite"`: the moment a choice is made
          this is the one place that says so in words a screen reader
          announces on its own, since the input's own text (the chosen
          option's name) does not carry the login that disambiguates it. */}
      <p id={`${id}-help`} role="status" aria-live="polite" className="text-small text-ink-3">
        {chosen
          ? selectedTemplate.replace("{name}", chosen.value.fullName).replace("{login}", chosen.value.login)
          : helpText}
      </p>

      {chosen ? (
        <Button
          type="button"
          variant="quiet"
          size="sm"
          className="self-start"
          onClick={() => {
            setChosen(null);
            setInputValue("");
            setOptions([]);
          }}
        >
          {changeText}
        </Button>
      ) : null}

      <input type="hidden" name={name} value={chosen?.value.userId ?? ""} />
    </div>
  );
}
