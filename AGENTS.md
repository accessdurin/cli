# Repository instructions

## Keep tests lean without losing confidence

- Proactively consolidate or remove redundant tests as much as possible to keep
  the suite lean and CI fast, without reducing consistency, confidence or coverage.
- Before adding a test, check whether an existing case can cover the behavior.
  Prefer extending it over repeating the same scenario and setup.
- Consolidate duplicate tests and share expensive setup only when test isolation,
  meaningful assertions, distinct behaviors, edge cases, security guarantees and
  regression coverage survive. Keep tests whose unique value cannot be shown to
  survive consolidation.
- Do not improve runtime by skipping tests, weakening assertions or narrowing
  discovery or coverage scope.
- For test-suite optimizations, compare before/after test counts, coverage where
  available and runtimes under comparable conditions. Distinguish measurements
  from estimates, and run `pnpm verify` before handing off changes.
