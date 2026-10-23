# lib/prs

This folder holds three kinds of review work:

- **GitHub pull requests**: `PRCard`, `PRDetail`, `PRChecks`, `PRChip` and their helpers.
- **Local changes**: the `Change*` components and `fixes.ts`, for a committed branch with a base and a head but no PR yet.
- **GitHub issues**: `IssueList`, `IssuePage` and `issues.test.ts`. These are issues from the repository, not room issues.

Room issues are a different record. They live in `../issues/`, and `localIssues.ts` there matches the Go feature `local-issues`.
