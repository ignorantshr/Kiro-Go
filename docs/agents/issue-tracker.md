# Issue tracker

Issue tracker integration is not configured for this repo.

Do not assume GitHub Issues, GitLab Issues, Jira, Linear, or local markdown issue files under `.scratch/`.

## Implications for skills

Skills that normally read from or publish to an issue tracker should not perform tracker operations in this repo unless the user explicitly provides a workflow for the current task.

This includes, for example:
- creating issues
- syncing triage state
- publishing PRDs to an issue tracker
- fetching a ticket by number

If the user wants issue-tracker behavior later, this file should be updated with the chosen workflow.
