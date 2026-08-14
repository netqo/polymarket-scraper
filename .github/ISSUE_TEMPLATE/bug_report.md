---
name: Bug report
about: The scraper produced wrong, missing, or untrustworthy output
title: "fix: "
labels: bug
---

## What happened

## What should have happened

## Reproduction

```bash
polymarket-scraper --tokens ... --duration ... --out ...
```

Exit code:

## Output evidence

The relevant slice of the output JSON (redact nothing; it is public market
data), plus the stderr log lines around the failure.

## Environment

- Version (`polymarket-scraper --version`):
- OS / arch:

## Severity check

- [ ] A book was reported as `status: "ok"` when it was not actually current
      (this is the highest-severity class of bug in this project; say so
      explicitly in the title if it applies)
