# PR comment trust

Dyna delivers comments from the signed-in GitHub owner, repository owners,
members, collaborators, and configured trusted bots. The default bot list is
`coderabbitai` and `copilot-pull-request-reviewer`. Bot names accept the `[bot]`
suffix. Set the list with:

```sh
dyna settings set pr.trustedBots '["coderabbitai","copilot-pull-request-reviewer"]'
```

An empty list trusts no bots. Trusted comments still pass the noise filter.
Dyna quotes non-owner comments as data, not instructions. The reviewer must
check their claims against the assigned code.

All other comments are held in SQLite and shown in home alerts and the PR room as
**Held: outside commenter**. They do not reach the author or reviewer.
Only the owner can select **Admit** or **Dismiss**, or run:

```sh
dyna git pr admit <number-or-url> <kind:id>
dyna git pr dismiss <number-or-url> <kind:id>
```

Admission applies to that exact comment version. An edited outside comment is
held again. Admission sends quoted data to the author and any active reviewer.

Agents do not publish GitHub comments or reviews. Their findings, verdicts,
and replies stay in Dyna. The owner can still post to GitHub.
