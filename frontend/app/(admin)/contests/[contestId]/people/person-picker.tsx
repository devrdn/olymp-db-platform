"use client";

import { useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { Combobox, type ComboboxOption } from "@/components/ui/combobox";
import { ApiError, request } from "@/lib/api/client";
import { MIN_DIRECTORY_QUERY_LENGTH } from "@/lib/api/people-terms";
import { debounce, type Debounced } from "@/lib/format/debounce";

/**
 * Debounce for the directory search: under ~200ms bursts are not collapsed,
 * past ~500ms results feel detached. Same as `users/filters.tsx`.
 */
const SEARCH_PAUSE_MS = 300;

/** A directory entry. `email` is empty for an account without one. */
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
 * Parsed by hand: importing the zod schemas would ship zod's parser to the
 * browser for four fields.
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
      // Omitted (`omitempty`) for an account with none; read as an empty
      // string.
      email: raw.email ?? "",
    }));
}

/** The popup's second line: the login, plus the email when there is one. */
function describeCandidate(candidate: Candidate): string {
  return candidate.email ? `${candidate.login} · ${candidate.email}` : candidate.login;
}

function toOption(candidate: Candidate): ComboboxOption<Candidate> {
  return { key: candidate.userId, value: candidate, label: candidate.fullName, description: describeCandidate(candidate) };
}

/**
 * Search by login, name or email, choose one, submit the id. Shared by the
 * staff form and the one-person participant form; bulk import is a separate
 * textarea.
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
  help,
  helpLabel,
}: {
  id: string;
  /** The hidden field's name. */
  name: string;
  contestId: string;
  label: string;
  placeholder: string;
  helpText: string;
  searchingText: string;
  noResultsText: string;
  searchFailedText: string;
  changeText: string;
  /** "Selected: {name} ({login})", filled in once someone is chosen. */
  selectedTemplate: string;
  /** Passed through to `Combobox`. */
  help?: string;
  helpLabel?: string;
}) {
  const [inputValue, setInputValue] = useState("");
  const [options, setOptions] = useState<Candidate[]>([]);
  const [chosen, setChosen] = useState<ComboboxOption<Candidate> | null>(null);
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState(false);
  // Drops a slower earlier reply that returns after a later one; debouncing
  // only spaces out the starts.
  const generation = useRef(0);
  // Built in an effect and called only from handlers, where refs may be read
  // (`react-hooks/refs`).
  const runSearchRef = useRef<Debounced<[string]> | null>(null);

  useEffect(() => {
    const debounced = debounce((query: string) => {
      const trimmed = query.trim();
      // Below the server's minimum the answer is always empty, so do not ask.
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
          // The person already sees the failure; log anything unexpected for a
          // developer. A rethrow would only be an unhandled rejection.
          if (!(error instanceof ApiError)) console.error("directory search failed", error);
        })
        .finally(() => {
          if (thisGeneration === generation.current) setLoading(false);
        });
    }, SEARCH_PAUSE_MS);

    runSearchRef.current = debounced;
    // Cancel a timer that would fire after unmount or a contest change.
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
          // Editing after a choice drops it, so an unseen id cannot be
          // submitted.
          if (chosen && next !== chosen.label) setChosen(null);
          runSearch(next);
        }}
        value={chosen}
        onValueChange={(next) => {
          setChosen(next);
          if (next) runSearchRef.current?.cancel();
        }}
        placeholder={placeholder}
        // "No matches" only once an answer came back for the current text.
        emptyMessage={
          loading || inputValue.trim().length < MIN_DIRECTORY_QUERY_LENGTH ? "" : noResultsText
        }
        statusMessage={status}
        describedBy={`${id}-help`}
        help={help}
        helpLabel={helpLabel}
      />

      {/* Announces the choice with its login, which the input text lacks. */}
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
