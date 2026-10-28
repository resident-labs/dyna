# Dyna's Pi fork

Dyna modified this vendored source. The upstream repository is `https://github.com/earendil-works/pi.git`. This subtree starts at tag `v1.4.0`, commit `4a080252f06caf63a525d7873c5d94d3c23887e0`. The upstream MIT license and all package license notices remain unchanged.

## Dyna changes

P0 adds this documentation and changes no upstream Pi file. It changes no Pi behavior and removes no Dyna code. Release packaging and later fork behavior live in separate Dyna commits.

Each later Pi behavior change must add an entry here and name the Dyna code it removes.

## Update

Run this command from the Dyna repository root, replacing `<tag>` with the new upstream tag:

```sh
git subtree pull --prefix=third_party/pi https://github.com/earendil-works/pi <tag> --squash
```

Keep the subtree commits in the Dyna history. Do not rebase or squash them away. Resolve any conflicts with Dyna's changes, then run the Pi build and coding-agent tests.

## Review

The subtree squash commit records the upstream commit. Apart from `DYNA.md`, the vendored files in P0 equal upstream `v1.4.0`. Reviewers can inspect the subtree commit instead of reading every vendored line.
