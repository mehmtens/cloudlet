# Cloudlet development rules

## Ponytail: full

Use the smallest correct solution after understanding the affected flow.

1. Skip speculative work (YAGNI).
2. Reuse an existing project pattern before adding code.
3. Prefer the standard library and native platform features.
4. Prefer an installed dependency over a new dependency.
5. Add the fewest files and lines that solve the verified problem.
6. Fix root causes in shared paths instead of patching individual symptoms.

Do not add single-use abstractions, speculative configuration, factories,
wrappers, or dependencies. Delete obsolete code when replacement makes it
unnecessary. Keep explanations and documentation proportional to the task.

Never simplify away security controls, authorization, input validation,
data-loss prevention, accessibility basics, migrations, or explicitly required
roadmap behavior. Non-trivial changes must keep the smallest relevant runnable
test. Verify claims before marking roadmap items complete.

Mark an intentional shortcut only when it has a real known ceiling:

```text
ponytail: <current limitation>; upgrade when <measurable condition>
```
