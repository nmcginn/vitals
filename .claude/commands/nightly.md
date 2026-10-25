---
description: Do tonight's increment of work on vitals: one complete, reviewable pull request.
---

# /nightly

You are doing one night's work on `vitals`. The goal is **one pull request** the maintainer can review over coffee: one *complete* change. Read `CLAUDE.md` first. It is the working agreement, and everything below assumes it.

## Non-negotiables

Check every change against these before you open the PR:

- **Standard library only.** No third-party Go modules, ever: not gopsutil, not the Prometheus client, not `golang.org/x/sys`. `go.mod` has no `require` lines.
- **Requests never collect.** Only the sampler goroutine calls `Collect`. Handlers read the store. Renderers are pure functions of a snapshot.
- **Rates are honest.** Deltas are divided by actual elapsed time. An unknown rate is `null` (or "—", or omitted in Prometheus), never 0 and never negative. Counters are 64-bit.
- **Spec numbers are fixed.** That covers the budgets (20 MB, 1% CPU, 50 KB, 80 columns), defaults (`127.0.0.1:9110`, `2s`, `15m`, 450 samples), timeouts, headers, and flag names. Change one only through an open question.
- **No private macOS API, and no shelling out** to `vm_stat`, `sysctl`, `df`, or anything else.
- **No JavaScript and no external requests** on the dashboard.
- **Read-only, unprivileged, nothing secret** in any output.
- **All C stays in `darwin_shim.c` and `.h`**, as calls and struct copies only. Logic goes in untagged Go that the Linux session can test.

## 1. Get oriented, feedback first

1. Read **Status** and **Open questions for the maintainer** in `docs/ROADMAP.md`.
2. List the open PRs on this repo, and read every review comment and every PR comment since the last nightly push.
3. **Review feedback on an open nightly PR comes first.** If a review asks for changes, tonight's work is addressing them. Check out that PR's own branch, make the changes with tests, push to *that* branch, and reply on the PR saying what changed. Don't open a second PR for it. If that leaves time and there is nothing else on the PR, carry on with step 2.
4. If the maintainer answered an open question (in the roadmap or a comment), record the answer in `docs/DECISIONS.md` as a new numbered entry, add a *Decided* note to the task it unblocks, and delete the question. Do this in tonight's PR.
5. If a merged PR left `master` red, fixing it is tonight's work.

## 2. Pick the task

Take the **first unchecked task** in `docs/ROADMAP.md` **Open** whose **Needs** have all merged and that isn't waiting on an open question.

- **Stack one deep, never two.** If the only startable task needs a PR that is open but not merged, you may branch from that PR's branch, but only from a PR that is itself based on `master`. Say so in the first line of the PR description ("Stacked on #N; review that first").
- Never start an item under **Ideas, not yet scheduled**.
- If nothing is startable, because everything is done, in review, or blocked, follow **When blocked** in `CLAUDE.md`, or open no PR and end with a short summary of why.

## 3. Build it

1. Branch from `master` (or the stacked branch) as `nightly/<yyyy-mm-dd>-<short-slug>`, using `date +%Y-%m-%d`.
2. Read the task's acceptance criteria and the relevant parts of `docs/PLAN.md` and `docs/SPEC.md`. Where they disagree, SPEC wins, and the disagreement goes in Open questions.
3. Write the tests first where you can. Every acceptance criterion should map to a named test. Tests are sentences (`t.Run("rate is null when the counter goes backwards", …)`) and cover the awkward cases `CLAUDE.md` lists that apply.
4. Implement. Keep each layer to its job (see **Architecture** in `CLAUDE.md`).
5. Run `make check` until it passes. Never weaken, skip, or delete a test to get there.

### Check the thing itself

Passing tests is not the same as working. Before writing the PR, do the ones that apply:

- **Run the binary.** Start it with `go run . -listen 127.0.0.1:0` (or a free port) and `curl` every endpoint the change touches: `/healthz`, `/api/v1/stats?pretty=1`, `/api/v1/history`, `/metrics`, `/?format=text`, and `/` with `-i` so you see the headers. Paste a trimmed sample of the real output into the PR. Look at the numbers: are percentages within 0 to 100, totals plausible for the container, rates `null` on the first request and real on a later one?
- **Read the golden diffs** line by line, as the reviewer will. A golden file regenerated without reading it is a test that checks nothing.
- **Dashboard changes:** render a page from a full fixture to a file. Check its size (`wc -c`, under 50 KB with 450 samples), that it has no `<script`, and that it has no `http`. Open it in headless Chromium (Playwright is available, using `executablePath: '/opt/pw-browsers/chromium'`) and screenshot it at 380 px and 1200 px, in light and dark modes. Look at the screenshots. Use a scratch directory and don't commit the tooling. This repo stays Go-only.
- **Text output:** `curl -s localhost:PORT/?format=text | awk '{ if (length($0) > 80) print "TOO WIDE: " $0 }'` prints nothing.
- **Prometheus output:** the grammar test passes, and the sample names in the output match PLAN's naming exactly, including the four the spec fixes.
- **Darwin changes:** this session can't compile them. After pushing, read the PR's check runs and wait for the `macos` job (it takes a few minutes). If it fails, read the log, fix it, and push again before you finish. If you can't read check results from this session, say so plainly in the PR, under what deserves the closest look.
- **Line budget:** report `find . -name '*.go' ! -name '*_test.go' | xargs wc -l | tail -1` and the shim's line count in the PR, with the change since the last PR. The spec's 1,000 to 1,200 is an estimate, not a budget (decision 29).

## 4. Audit before writing the PR

Re-read `CLAUDE.md` "What one pull request means". Then, for every "follow-up", "later", "TODO", or "not yet" in your notes, code, or draft description, ask: **would the reviewer expect this to be in the PR already?** If so, it is the rest of tonight's task. Do it now.

A follow-up survives only if it lands on a surface this PR does not touch, turns on a decision that is the maintainer's to make, or is a night's work on its own. Add each survivor to `docs/ROADMAP.md` as a task with Needs and Acceptance, or as an open question.

If you finish early, finish *this* change better: more of the awkward cases, clearer errors, a tighter allocation profile. Don't start a second task.

## 5. Update the docs, in the same PR

- `docs/ROADMAP.md`: tick the task and move it to **Done** as **one line** naming what landed and its decision numbers. Update **Status** (current, next up, open PRs). Add or remove open questions.
- `docs/DECISIONS.md`: a new numbered entry for each choice someone might later re-litigate, a few lines each. Never renumber. Amend a reversed decision rather than deleting it.
- `docs/ARCHITECTURE.md`: keep **Shape** as-built if modules moved, appeared, or disappeared.
- `README.md`: if user-visible behaviour changed (flags, endpoints, install).

Keep these files short. The long explanation goes in the PR description, not the docs. Every future session reads the docs.

## 6. Open the PR

1. Commit with an imperative subject (`Add darwin memory collector with pressure level`) and a body saying what and why.
2. Push the branch: `git push -u origin nightly/<date>-<slug>`.
3. Open the PR against `master` (or the stacked branch), filling in `.github/pull_request_template.md`. The description is the entire handover:
   - what changed and why, in a few sentences;
   - each acceptance criterion, and exactly how it was verified (test name, command, or "*(maintainer)*: please try X");
   - what deserves the closest look, putting what you're least sure of first. **A flagged uncertainty is far more useful than a confident-sounding guess;**
   - real output from running it.
4. If the PR touches darwin code, wait for the `macos` CI job as described above.
5. Never merge it. Never push to `master`.

End the session with a short summary: the PR link, the task, and anything the maintainer needs to decide.
