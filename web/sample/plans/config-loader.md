# Migrate config loader

**Summary:** Replace the hand-rolled config loader with the shared loader in repo-a.

## Scope / non-goals

- **writes:** `repo-a/src/config/`
- **branch:** `companion/T-016`

## Steps

### s1 — Port loader
- **agent:** worker-medium
- **writes:** `repo-a/src/config/`
- Move the loader across and keep the public API stable.

### s2 — Cut over call sites
- **depends on:** s1
- **agent:** worker-medium
- **writes:** `repo-a/src/config/`
- Update call sites and remove the old loader.
