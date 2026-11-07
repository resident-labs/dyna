<!-- coderabbit-ai-generated:v1 -->

<!-- This is an auto-generated comment: summarize by coderabbit.ai -->
<!-- review_stack_entry_start -->

<a href="https://app.coderabbit.ai/change-stack/arismoko/horizon/pull/1624?cs_source=review_comment"><picture><source media="(prefers-color-scheme: dark)" srcset="https://storage.googleapis.com/coderabbit_public_assets/review-stack-buttons-v1/explain-change-dark.svg"><source media="(prefers-color-scheme: light)" srcset="https://storage.googleapis.com/coderabbit_public_assets/review-stack-buttons-v1/explain-change.svg"><img src="https://storage.googleapis.com/coderabbit_public_assets/review-stack-buttons-v1/explain-change.svg" alt="Explain Change →" height="32"></picture></a>

<!-- review_stack_entry_end -->
<!-- walkthrough_start -->

### Walkthrough

The pull request adds horizon-mod-runtime to the workspace. The crate provides shared attachment types and registry-key services. Client, renderer, protocol, world, and API code now reference these runtime types. Registry resolution now receives identifiers and lookup callbacks. The changes also add registry tests and tooling to replay and verify the extraction.

**Possibly related issues**
- arismoko/horizon issue 771: Describes the mod-runtime crate as an API boundary step; this change adds that crate and extracts runtime services.
- arismoko/horizon issue 1608: Calls for moving attachments, the query index, and key tables into horizon-mod-runtime; this change extracts those components.
- arismoko/horizon issue 1514: Concerns attachment lifetime and observer identity; this change moves attachment lifetime and observer infrastructure, but does not describe the issue’s replacement behavior.
- arismoko/horizon issue 796: Concerns the Part instance-data contract; this change moves the attachment types that the issue identifies as supporting that contract.

<!-- walkthrough_end -->
<!-- change_assessment_start -->
<!-- change_assessment_commit:"5b3f83d9d7c4e65d78ac3fbcae9104ea9b802c93" -->
<!-- change_assessment_priority:{"version":1,"urgency":"p2","manual":false} -->

<!-- change_assessment_end -->
<details>
<summary><strong>Merge Risk:</strong> <strong><picture><img src="https://storage.googleapis.com/coderabbit_public_assets/review-comment-icons-v1/risk-low-16.svg" width="16" height="16" align="absmiddle" alt=""></picture> Low</strong> · up to <code>5b3f8</code></summary>
<dl>
<dd>
<br>

<!-- final_review_risk_start -->
<!-- final_review_risk_label
**Merge Risk:** **🔵 Low** · up to `5b3f8`
-->
<!-- final_review_risk_coverage:{"sourceCommitId":"5b3f83d9d7c4e65d78ac3fbcae9104ea9b802c93","coveredCommitId":"5b3f83d9d7c4e65d78ac3fbcae9104ea9b802c93","kind":"reviewed"} -->

This refactor moves attachment storage and registry-key services into a new crate without intended behavior change. The new effect resolver can panic if called with an ID list that does not match the registry's indices, but the current caller passes the full list. Merge is low risk, and a bounds check on the resolver would be a worthwhile follow-up.
<!-- final_review_risk_end -->

</dd>
</dl>
</details>
<!-- pre_merge_checks_walkthrough_start -->

<details>
<summary><strong>Pre-merge checks: <sup><picture><img src="https://storage.googleapis.com/coderabbit_public_assets/pre-merge-checks-counter-passed-14-v2.svg" width="14" height="14" align="middle" alt="Passed" title="Passed"></picture></sup> 3 · <sup><picture><img src="https://storage.googleapis.com/coderabbit_public_assets/pre-merge-checks-counter-failed-14-v2.svg" width="14" height="14" align="middle" alt="Failed" title="Failed"></picture></sup> 1 · <sup><picture><img src="https://storage.googleapis.com/coderabbit_public_assets/pre-merge-checks-counter-inconclusive-14-v2.svg" width="14" height="14" align="middle" alt="Inconclusive" title="Inconclusive"></picture></sup> 1</strong></summary>
<dl>
<dd>
<br>

<strong><sup><picture><img src="https://storage.googleapis.com/coderabbit_public_assets/pre-merge-checks-counter-failed-14-v2.svg" width="14" height="14" align="middle" alt="Failed" title="Failed"></picture></sup> Failed checks: 1 warning · 1 inconclusive</strong>

|     Check name     |                                                   Status                                                   | Explanation                                                                                                                                                                                               | Resolution                                                                                                                                                                        |
| :---------------- | :--------------------------------------------------------------------------------------------------------: | :-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | :-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
|  Description check |      ![Warning](https://storage.googleapis.com/coderabbit_public_assets/pre-merge-checks-warning.svg)      | The description explains the change and reports tests, but omits several required template sections: Kind, Vanilla reference, Other projects' code, and Self-check.                                       | Select a Kind. Add the Vanilla reference and Other projects' code sections, using the applicable values. Paste the output of `python3 tools/pr_ready.py` in a Self-check section. |
| Docstring Coverage | ![Inconclusive](https://storage.googleapis.com/coderabbit_public_assets/pre-merge-checks-inconclusive.svg) | Docstring coverage is 57.50% which is insufficient. The required threshold is 80.00%. Docstring coverage is scoped to functions touched by this diff with cyclomatic complexity of at least 2. Documenta… | Write docstrings for the functions missing them to satisfy the coverage threshold.                                                                                                |

<details>
<summary><strong><sup><picture><img src="https://storage.googleapis.com/coderabbit_public_assets/pre-merge-checks-counter-passed-14-v2.svg" width="14" height="14" align="middle" alt="Passed" title="Passed"></picture></sup> Passed checks: 3 passed</strong></summary>
<dl>
<dd>
<br>

|         Check name         |                                             Status                                             | Explanation                                                               |
| :------------------------ | :--------------------------------------------------------------------------------------------: | :------------------------------------------------------------------------ |
|         Title check        | ![Passed](https://storage.googleapis.com/coderabbit_public_assets/pre-merge-checks-passed.svg) | The title identifies the new runtime crate and its main responsibilities. |
|     Linked Issues check    | ![Passed](https://storage.googleapis.com/coderabbit_public_assets/pre-merge-checks-passed.svg) | Check skipped because no linked issues were found for this pull request.  |
| Out of Scope Changes check | ![Passed](https://storage.googleapis.com/coderabbit_public_assets/pre-merge-checks-passed.svg) | Check skipped because no linked issues were found for this pull request.  |

</dd>
</dl>
</details>

<details>
<summary><strong>Full details: Docstring Coverage</strong></summary>
<dl>
<dd>
<br>

**Explanation**

Docstring coverage is 57.50% which is insufficient. The required threshold is 80.00%. Docstring coverage is scoped to functions touched by this diff with cyclomatic complexity of at least 2. Documentation-only edits and deletions within surviving functions are included; unchanged diff context and fully deleted functions are excluded. Analyzed 40 functions across 46 files. (18 skipped: 13 unsupported, 4 too large, 1 over the file limit.)

**Resolution**

Write docstrings for the functions missing them to satisfy the coverage threshold.

</dd>
</dl>
</details>

</dd>
</dl>
</details>

<!-- pre_merge_checks_walkthrough_end -->
<!-- autopilot:start -->
- [ ] <!-- {"checkboxId":"2708ad07-9f24-4260-9c11-7dc76a49f2e3"} --> <strong title="Keep fixing CodeRabbit findings and required CI, and resolving merge conflicts">Autofix</strong> · Keep fixing CodeRabbit findings and required CI, and resolving merge conflicts
<!-- autopilot:end -->
<!-- tips_start -->



Thanks for using [CodeRabbit](https://coderabbit.ai?utm_source=oss&utm_medium=github&utm_campaign=arismoko/horizon&utm_content=1624)! It's free for OSS, and your support helps us grow. If you like it, consider giving us a shout-out.

<details>
<summary><strong>Share</strong></summary>
<dl>
<dd>
<br>

- [X](https://twitter.com/intent/tweet?text=I%20just%20used%20%40coderabbitai%20for%20my%20code%20review%2C%20and%20it%27s%20fantastic%21%20It%27s%20free%20for%20OSS%20and%20offers%20a%20free%20trial%20for%20the%20proprietary%20code.%20Check%20it%20out%3A&url=https%3A//coderabbit.ai)
- [Mastodon](https://mastodon.social/share?text=I%20just%20used%20%40coderabbitai%20for%20my%20code%20review%2C%20and%20it%27s%20fantastic%21%20It%27s%20free%20for%20OSS%20and%20offers%20a%20free%20trial%20for%20the%20proprietary%20code.%20Check%20it%20out%3A%20https%3A%2F%2Fcoderabbit.ai)
- [Reddit](https://www.reddit.com/submit?title=Great%20tool%20for%20code%20review%20-%20CodeRabbit&text=I%20just%20used%20CodeRabbit%20for%20my%20code%20review%2C%20and%20it%27s%20fantastic%21%20It%27s%20free%20for%20OSS%20and%20offers%20a%20free%20trial%20for%20proprietary%20code.%20Check%20it%20out%3A%20https%3A//coderabbit.ai)
- [LinkedIn](https://www.linkedin.com/sharing/share-offsite/?url=https%3A%2F%2Fcoderabbit.ai&mini=true&title=Great%20tool%20for%20code%20review%20-%20CodeRabbit&summary=I%20just%20used%20CodeRabbit%20for%20my%20code%20review%2C%20and%20it%27s%20fantastic%21%20It%27s%20free%20for%20OSS%20and%20offers%20a%20free%20trial%20for%20proprietary%20code)

</dd>
</dl>
</details>

<!-- tips_end -->