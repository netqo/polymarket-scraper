---
name: Feature request
about: Propose a change to the scraper's behaviour or output contract
title: "feat: "
labels: enhancement
---

## Problem

What the consuming agent cannot do today.

## Proposal

## Scope check

The scraper is a dumb, honest pipe. It does not filter, rank, or score markets,
compute edges or fees, fetch Gamma metadata, or authenticate (requirement G).

- [ ] This proposal stays inside that boundary

## Output contract impact

- [ ] No change to the output JSON shape
- [ ] Additive change (new optional field), `schema_version` unchanged
- [ ] Breaking change, `schema_version` must be bumped and `SCHEMA.md` updated
      in the same commit
