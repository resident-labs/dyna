<!-- This is an auto-generated comment: summarize by coderabbit.ai -->
<!-- review_stack_entry_start -->

<a href="https://app.coderabbit.ai/change-stack/resident-labs/dyna/pull/537?scope=REDACTED&amp;cs_source=review_comment"><picture><source media="(prefers-color-scheme: dark)" srcset="https://storage.googleapis.com/coderabbit_public_assets/review-stack-in-coderabbit-ui-dark.svg?v=3"><source media="(prefers-color-scheme: light)" srcset="https://storage.googleapis.com/coderabbit_public_assets/review-stack-in-coderabbit-ui.svg?v=3"><img src="https://storage.googleapis.com/coderabbit_public_assets/review-stack-in-coderabbit-ui.svg?v=3" alt="Review in Change Stack →" height="32"></picture></a>

<!-- review_stack_entry_end -->
<!-- recent_review_start -->

<details>
<summary><strong>ℹ️ Recent review info</strong></summary>
<dl>
<dd>

<details>
<summary><strong>⚙️ Run configuration</strong></summary>
<dl>
<dd>

- **Configuration used**: Organization UI
- **Review profile**: ASSERTIVE
- **Plan**: Free
- **Run ID**: `5d399ed5-f567-4626-9b0e-0548409c1e4f`

</dd>
</dl>
</details>

<details>
<summary><strong>📥 Commits</strong></summary>
<dl>
<dd>

Reviewing files that changed from the base of the PR and between be756e955ab1e661d707535474135ea95c556756 and ea3bfb4fb9cec34e2ec005c1e5b731085b3e2723.

</dd>
</dl>
</details>

<details>
<summary><strong>⛔ Files ignored due to path filters: 1</strong></summary>
<dl>
<dd>

* `web/src/lib/settings/harnesses/icons/cursor.svg` is excluded by `!**/*.svg`

</dd>
</dl>
</details>

<details>
<summary><strong>📒 Files selected for processing: 105</strong></summary>
<dl>
<dd>

* `README.md`
* `docs/credential-refresh.md`
* `docs/desktop/ACCOUNTS.md`
* `docs/desktop/HARNESSES.md`
* `docs/harness-config-formats.md`
* `docs/private-harness-runtimes.md`
* `docs/reasoning-and-compaction.md`
* `docs/session-rules.md`
* `docs/x-subscription-usage.md`
* `install.sh`
* `internal/account/native/antigravity_test.go`
* `internal/account/native/codex.go`
* `internal/account/native/credential_metadata.go`
* `internal/account/native/cursor.go`
* `internal/account/native/cursor_refresh.go`
* `internal/account/native/cursor_refresh_test.go`
* `internal/account/native/cursor_test.go`
* `internal/account/native/cursor_usage.go`
* `internal/account/native/cursor_usage_test.go`
* `internal/account/native/saved_antigravity.go`
* `internal/account/native/saved_cursor.go`
* `internal/account/native/saved_direct_test.go`
* `internal/account/service/adapters/adapters.go`
* `internal/account/service/adapters/cursor.go`
* `internal/account/service/adapters/cursor_test.go`
* `internal/account/service/runtime_accounts_test.go`
* `internal/account/service/service_test.go`
* `internal/cli/profiles/check_test.go`
* `internal/cli/profiles/defaults/profiles.json`
* `internal/cli/profiles/profiles.go`
* `internal/cli/profiles/profiles_test.go`
* `internal/cli/runtime/command.go`
* `internal/configsvc/configsvc_test.go`
* `internal/harness/attribution.go`
* `internal/harness/config/backup.go`
* `internal/harness/config/config.go`
* `internal/harness/config/config_adapter.go`
* `internal/harness/config/config_cursor.go`
* `internal/harness/harness.go`
* `internal/harness/harness_test.go`
* `internal/harness/health_test.go`
* `internal/harness/home/discovery.go`
* `internal/harness/home/home_test.go`
* `internal/harness/invocation.go`
* `internal/harness/path/resolve_test.go`
* `internal/harness/permissions.go`
* `internal/harness/runtime/accounts.go`
* `internal/harness/runtime/accounts_home.go`
* `internal/harness/runtime/accounts_home_test.go`
* `internal/harness/runtime/accounts_switch_test.go`
* `internal/harness/runtime/adapter.go`
* `internal/harness/runtime/adapter_cursor.go`
* `internal/harness/runtime/adapter_cursor_test.go`
* `internal/harness/runtime/credential_refresh_test.go`
* `internal/harness/runtime/firstuse_test.go`
* `internal/harness/runtime/generation.go`
* `internal/harness/runtime/generation_lifecycle_test.go`
* `internal/harness/runtime/generation_test.go`
* `internal/harness/runtime/global_rules_test.go`
* `internal/harness/runtime/native_environment_test.go`
* `internal/harness/runtime/owner_login_import.go`
* `internal/harness/runtime/owner_login_import_test.go`
* `internal/harness/runtime/ready_test.go`
* `internal/harness/runtime/recommended.go`
* `internal/harness/runtime/recommended_test.go`
* `internal/harness/runtime/retired.go`
* `internal/harness/runtime/retired_test.go`
* `internal/harness/runtime/runtime.go`
* `internal/harness/runtime/runtime_home_test.go`
* `internal/harness/steerhook_test.go`
* `internal/harness/toolboundary_test.go`
* `internal/notify/child_failure_test.go`
* `internal/profile/compact.go`
* `internal/profile/compact_test.go`
* `internal/profile/dialects_test.go`
* `internal/profile/harnesses.go`
* `internal/profile/profile.go`
* `internal/profile/profile_test.go`
* `internal/subagent/keeper_test.go`
* `internal/workeroutput/adapters.go`
* `internal/workeroutput/boundaries.go`
* `internal/workeroutput/capabilities.go`
* `internal/workeroutput/conformance_test.go`
* `internal/workeroutput/output.go`
* `internal/workeroutput/output_test.go`
* `internal/workeroutput/records.go`
* `internal/workeroutput/records_test.go`
* `internal/workeroutput/result_error.go`
* `internal/workeroutput/result_error_test.go`
* `internal/workeroutput/results.go`
* `internal/workeroutput/testdata/cursor-text.jsonl`
* `internal/workeroutput/testdata/cursor-tools.jsonl`
* `internal/workeroutput/usage_normalization.go`
* `main.go`
* `web/src/lib/settings/ProfileWizard.svelte`
* `web/src/lib/settings/ProfilesPane.svelte`
* `web/src/lib/settings/harnesses/HarnessIcon.svelte`
* `web/src/lib/settings/harnesses/icons/README.md`
* `web/src/lib/settings/harnesses/state.js`
* `web/src/lib/settings/harnesses/state.test.js`
* `web/src/lib/settings/health.test.js`
* `web/src/lib/settings/profiles/state.js`
* `web/src/lib/settings/usage/UsagePane.svelte`
* `web/src/lib/settings/usage/state.js`
* `web/src/lib/settings/usage/state.test.js`

</dd>
</dl>
</details>

<details>
<summary><strong>💤 Files with no reviewable changes: 41</strong></summary>
<dl>
<dd>

* internal/account/service/adapters/cursor_test.go
* docs/desktop/ACCOUNTS.md
* internal/workeroutput/testdata/cursor-tools.jsonl
* internal/harness/runtime/generation.go
* internal/account/native/cursor.go
* internal/notify/child_failure_test.go
* internal/account/native/cursor_test.go
* internal/profile/compact_test.go
* internal/profile/compact.go
* internal/harness/runtime/adapter.go
* internal/harness/config/config_adapter.go
* internal/account/native/cursor_usage_test.go
* docs/session-rules.md
* internal/workeroutput/testdata/cursor-text.jsonl
* internal/harness/invocation.go
* internal/workeroutput/output.go
* internal/workeroutput/capabilities.go
* internal/workeroutput/result_error.go
* internal/harness/runtime/adapter_cursor.go
* internal/harness/runtime/adapter_cursor_test.go
* internal/account/native/cursor_usage.go
* internal/account/service/adapters/adapters.go
* internal/workeroutput/result_error_test.go
* internal/harness/toolboundary_test.go
* internal/harness/home/discovery.go
* internal/workeroutput/adapters.go
* docs/harness-config-formats.md
* internal/harness/config/config_cursor.go
* internal/cli/profiles/defaults/profiles.json
* internal/account/service/adapters/cursor.go
* internal/account/native/saved_cursor.go
* internal/account/native/cursor_refresh_test.go
* internal/harness/permissions.go
* internal/workeroutput/records.go
* internal/workeroutput/records_test.go
* internal/account/native/credential_metadata.go
* internal/account/native/cursor_refresh.go
* internal/harness/runtime/recommended.go
* install.sh
* internal/workeroutput/results.go
* internal/profile/harnesses.go

</dd>
</dl>
</details>

**Included review availability:** This review used your included allowance. Your plan provides up to 1 included review per hour; 0 remain after this review.

</dd>
</dl>
</details>





<!-- recent_review_end -->
<!-- walkthrough_start -->

### Walkthrough

The pull request retires Cursor Agent across native accounts, runtime adapters, invocation, output parsing, profiles, documentation, and web settings. It removes Cursor credential refresh, usage, model discovery, installation, and stream JSON handling. Stored Cursor profiles are filtered during loading. Runtime garbage collection removes retired Cursor accounts and directories. Documentation and tests are updated for the remaining harnesses, including Grok and Pi.

<!-- walkthrough_end -->
<!-- autopilot:start -->
- [ ] <!-- {"checkboxId":"2708ad07-9f24-4260-9c11-7dc76a49f2e3"} --> <strong title="Keep fixing CodeRabbit findings and required CI, and resolving merge conflicts">Autofix</strong> · Keep fixing CodeRabbit findings and required CI, and resolving merge conflicts
<!-- autopilot:end -->
<!-- tips_start -->



> [!NOTE]
> <details>
> <summary><strong>🎁 Summarized by CodeRabbit Free</strong></summary>
> <dl>
> <dd>
> 
> Your organization is on the Free plan. CodeRabbit will generate a high-level summary and a walkthrough for each pull request. For a comprehensive line-by-line review, please upgrade your subscription to CodeRabbit Essentials by visiting <https://app.coderabbit.ai/settings/billing>.
> 
> </dd>
> </dl>
> </details>


<sub>Comment `@coderabbitai help` to get the list of available commands.</sub>

<!-- tips_end -->
