<!-- One complete change. If this is stacked on another PR, say so on the first line: "Stacked on #N; review that first." -->

## What and why

<!-- The task (M<N> from docs/ROADMAP.md, or review feedback on #N), what changed, and why, in a few sentences. -->

## Acceptance criteria

<!-- Each criterion from the roadmap task, and exactly how it was verified: a test name, a command and its output, or "(maintainer) please try …" for checks that need the Mac Mini, promtool, or a real browser. -->

- [ ] …

## Look closely at

<!-- What you're least sure of goes first. A flagged uncertainty is far more useful than a confident-sounding guess. -->

## Output from running it

<!-- A trimmed sample of real output: curl of the endpoints touched, a golden diff excerpt, screenshots for dashboard changes. -->

## Checklist

- [ ] `make check` passes locally
- [ ] Darwin code touched? The `macos` CI job is green (or this says why it couldn't be checked)
- [ ] No `require` lines in `go.mod`; no third-party modules; no shelling out; no private macOS API
- [ ] Requests never trigger collection; renderers stay pure
- [ ] Unknown rates are `null` / "—" / omitted — never 0, never negative
- [ ] Spec numbers unchanged (budgets, defaults, timeouts, headers, flags)
- [ ] Awkward cases tested (first sample, counter reset, group failure, empty lists, escaping, 80 columns — whichever apply)
- [ ] Golden-file diffs read line by line
- [ ] Go line count reported, with the change since the last PR (an estimate, not a budget: decision 29)
- [ ] `docs/ROADMAP.md` (Done line + Status), `docs/DECISIONS.md`, `docs/ARCHITECTURE.md`, `README.md` updated as needed

<!-- Follow-up audit: for every "follow-up", "later" or "TODO" above, would the reviewer expect it in this PR already? If so, it isn't a follow-up — finish it. -->
