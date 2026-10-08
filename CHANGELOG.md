# Changelog

## [1.3.0](https://github.com/compliance-framework/workflows/compare/v1.2.0...v1.3.0) (2026-10-08)


### Features

* **repo-settings:** manage the workflows repo; tag ruleset, strict review and Actions settings ([8e4c27b](https://github.com/compliance-framework/workflows/commit/8e4c27b2a824715c293364128670d9c6361dadfa))
* **repo-settings:** manage the workflows repo; tag ruleset, strict review and Actions settings ([ddf5d7c](https://github.com/compliance-framework/workflows/commit/ddf5d7c92cdde97c3d7cea914f40cbfced3b528f))
* **repo-settings:** release environment for the ccf-release-bot secrets ([9b8311e](https://github.com/compliance-framework/workflows/commit/9b8311e7c03804e28b785d6bfd2d7ad63b31ef0e))
* **repo-settings:** release environment for the ccf-release-bot secrets ([6a44da0](https://github.com/compliance-framework/workflows/commit/6a44da050f0f2a9a382101c2457387de1d9ddc2f))


### Bug Fixes

* **ccf-bump:** move workflows pins only to bot releases on the default branch ([2a9b345](https://github.com/compliance-framework/workflows/commit/2a9b345009652e8fb1006baf7a778b5f52b36172))
* **ccf-bump:** move workflows pins only to bot releases on the default branch ([86b1188](https://github.com/compliance-framework/workflows/commit/86b11887aeb6096e32b705efa36dece825cb4897))
* **ci:** pass ci / required when release-checks is skipped ([d628ff2](https://github.com/compliance-framework/workflows/commit/d628ff2c8ac629e451f5aed1c2795eeab6bcc23d))
* **ci:** pass ci / required when release-checks is skipped ([1e26f5f](https://github.com/compliance-framework/workflows/commit/1e26f5fa51f3639a32af471ac32cd67a28034c67))
* **ci:** pass ci / required when release-checks is skipped ([7ded49c](https://github.com/compliance-framework/workflows/commit/7ded49c636195ae57dcd73481faad999e504b5fb))
* **train:** never adopt a fork's PR as the release PR ([f1565f7](https://github.com/compliance-framework/workflows/commit/f1565f70b08952d6c04263445e7e18dc4acc77af))
* **train:** never adopt a fork's PR as the release PR ([443ecdd](https://github.com/compliance-framework/workflows/commit/443ecdd710cf5ec432452a452e2076fc9b5d4126))

## [1.2.0](https://github.com/compliance-framework/workflows/compare/v1.1.1...v1.2.0) (2026-10-08)


### Features

* **attention:** needs-human digest as a card ([d7544af](https://github.com/compliance-framework/workflows/commit/d7544afb737bf58cd7fcce083c55b19d4626f4fe))
* **notify:** mark a closed PR's cards handled and close its incident ([720ffe4](https://github.com/compliance-framework/workflows/commit/720ffe447126c968a22e7831665804e451926fb6))
* **notify:** rich incident cards edited in place ([b7c50cd](https://github.com/compliance-framework/workflows/commit/b7c50cd8ccde256332913910ca4b28408286d9f8))
* **slack:** Block Kit message kit with post, reply and edit in place ([47698b0](https://github.com/compliance-framework/workflows/commit/47698b041d2e37aa3aa78b87e3e98ec5cd48a1f3))
* **slack:** incident, needs-human, train board and digest cards ([950e2d6](https://github.com/compliance-framework/workflows/commit/950e2d6afb20b484b6371cdf02734dc4d227369e))
* **slack:** slack-preview workflow posting sample cards to the real channels ([7de3ceb](https://github.com/compliance-framework/workflows/commit/7de3cebc0b6c8616244bfc0ab29b1a507ac999d0))
* **train:** live release board and digest draft to #ccf-release-digests ([3c57a18](https://github.com/compliance-framework/workflows/commit/3c57a1807cd3076acf46fbae01e2cac615430475))
* **train:** live release board and digest draft to #ccf-release-digests ([af7237d](https://github.com/compliance-framework/workflows/commit/af7237d2ed5db0d665fc83d8577549f299507459))


### Bug Fixes

* **train:** build PR links from GITHUB_SERVER_URL; tighten the closed-PR guard test ([fe48829](https://github.com/compliance-framework/workflows/commit/fe48829a89714fb7ddd56f9b5e70539c84189d47))
* **train:** gate merges on required checks only; cancelled is never a failure ([43b6325](https://github.com/compliance-framework/workflows/commit/43b63250a40555ca72df05cc8023f51eb61ff068))
* **train:** gate merges on required checks only; cancelled is never a failure ([bc56e95](https://github.com/compliance-framework/workflows/commit/bc56e95415f1e042d160c2fa506c70b2ce9c28b1))
* **train:** link PRs by repo in the train issue and Slack; skip CI and notify on closed PRs ([c3b67f2](https://github.com/compliance-framework/workflows/commit/c3b67f25c43f276946ed43a7e9527c41a2ff751f))
* **train:** link PRs by repo; skip CI and notify on closed PRs ([3e04241](https://github.com/compliance-framework/workflows/commit/3e042412cb3f48a3373ac5cfcea16940292a24d3))
* **train:** wait only for release-please release tags, not floating major tags ([31c2168](https://github.com/compliance-framework/workflows/commit/31c2168b0a679d791e672251e721b5261b113780))
* **train:** wait only for release-please release tags, not floating major tags ([a08ae40](https://github.com/compliance-framework/workflows/commit/a08ae404856dc07f2e8df7b0105b025afcfd98f0))

## [1.1.1](https://github.com/compliance-framework/workflows/compare/v1.1.0...v1.1.1) (2026-10-08)


### Bug Fixes

* **ccf-bump:** leave a PR merged or closed during the merge pass's wait ([9469d9b](https://github.com/compliance-framework/workflows/commit/9469d9bfe19a28da6a5bf2403fd00c1209f5b53e))
* **ccf-bump:** merge its own PRs as the release bot instead of GitHub auto-merge ([3c1d925](https://github.com/compliance-framework/workflows/commit/3c1d925a49f2e221f47221ac97669814b5d6b1b7))
* **ccf-bump:** merge its own PRs as the release bot instead of GitHub auto-merge ([880fbaa](https://github.com/compliance-framework/workflows/commit/880fbaa76063f52d531dc5b3f2a32044696dbeb2))
* **ccf-bump:** merge only when the newest ci / required run passed ([20aa48d](https://github.com/compliance-framework/workflows/commit/20aa48d2ca86e93288da852a2d24acfa31ae701e))
* **ccf-bump:** merge only when the newest ci / required run passed ([a88abf9](https://github.com/compliance-framework/workflows/commit/a88abf9215ac4469e44e717e64cf6a338286ea70))
* **ccf-bump:** scheduled sync reads repos.mock.yaml until go-live ([fc0d779](https://github.com/compliance-framework/workflows/commit/fc0d779257482818f17b1150be95cffb0ee14f80))
* **ccf-bump:** scheduled sync reads repos.mock.yaml until go-live ([ab165c0](https://github.com/compliance-framework/workflows/commit/ab165c02a552b7f22abd2a929d4a6f8980bbabd4))
* **train:** judge checks by their newest run, treat a pending required check as waiting ([86f2812](https://github.com/compliance-framework/workflows/commit/86f2812adacd457a89657a7248308117bb17fa57))
* **train:** judge checks by their newest run, treat a pending required check as waiting ([f437fef](https://github.com/compliance-framework/workflows/commit/f437fef0d2343c99dc356c60fd17bbf739734973))

## [1.1.0](https://github.com/compliance-framework/workflows/compare/v1.0.0...v1.1.0) (2026-10-08)


### Features

* **attention:** rules and read-only GitHub client for the attention digest ([cd3346b](https://github.com/compliance-framework/workflows/commit/cd3346b790295ec1d0f4be1b66013f53a32b7f14))
* **attention:** weekly attention digest to SLACK_CHANNEL_NEEDS_HUMAN ([85b5382](https://github.com/compliance-framework/workflows/commit/85b5382f224213542b6abbe683e2d8989256ee13))
* **attention:** weekly attention-digest workflow posting to SLACK_CHANNEL_NEEDS_HUMAN ([37f50f8](https://github.com/compliance-framework/workflows/commit/37f50f8ac5f859885a734683b05ba8548fb94565))
* **ccf-bump:** title a workflows-only bump ci(deps) so it proposes no release ([b12b068](https://github.com/compliance-framework/workflows/commit/b12b0687d01207fbf78a6b9142cdb4c7dd59314e))
* **notify:** post bot PRs that need a human to SLACK_CHANNEL_NEEDS_HUMAN once ([a9378d5](https://github.com/compliance-framework/workflows/commit/a9378d5947e58035b364639a2a2e96dc29d7d2f1))


### Bug Fixes

* **attention:** release-please PRs are never stale; they wait for the train ([3069a93](https://github.com/compliance-framework/workflows/commit/3069a93d21226efee0f3eaf142a3fabd57002bb3))
* **renovate:** indirect Go module updates are fix(deps), so they release ([fbee0b2](https://github.com/compliance-framework/workflows/commit/fbee0b2febfbcdff12749b4ba8654925ea3d5861))

## 1.0.0 (2026-10-08)


### Features

* **ccf-bump:** find and rewrite internal dependency pins ([26e446f](https://github.com/compliance-framework/workflows/commit/26e446f10d9c55cf05cdb32023e8ee6745034282))
* **ccf-bump:** helm pins and the GitHub client ([2a402f7](https://github.com/compliance-framework/workflows/commit/2a402f755cfb8a7fee0033c5c7bfa211963a4d13))
* **ccf-bump:** pin shared workflows to the release commit SHA ([8e713a9](https://github.com/compliance-framework/workflows/commit/8e713a9837b7e9677ee5a4e8c881d57b6dc16fac))
* **ccf-bump:** pin shared workflows to the release commit SHA ([aedcb44](https://github.com/compliance-framework/workflows/commit/aedcb4490669561f239bb2c6dffd5969e5784946))
* **ccf-bump:** the ccf-bump CLI and the ccf-bump-sync workflow ([40c7736](https://github.com/compliance-framework/workflows/commit/40c7736e294c643b4ffab5bce5fe6f8e84ba2377))
* **ci:** Go service and library CI (ci-go-service, ci-go-lib) ([#6](https://github.com/compliance-framework/workflows/issues/6)) ([9b60ae0](https://github.com/compliance-framework/workflows/commit/9b60ae0a8e3799b29817d9c76d10613b52a141d5))
* **ci:** plugin and policy CI (ci-go-plugin, ci-policies) ([#5](https://github.com/compliance-framework/workflows/issues/5)) ([7f468c3](https://github.com/compliance-framework/workflows/commit/7f468c3bf123415699352deeb36d16e0947b0311))
* **ci:** shared CI building blocks (ci-common, notify-failure) ([#4](https://github.com/compliance-framework/workflows/issues/4)) ([26edf5b](https://github.com/compliance-framework/workflows/commit/26edf5bc7df9a4a54b22d1d3ca2108771b7a006f))
* **ci:** ui, helm and action CI ([#7](https://github.com/compliance-framework/workflows/issues/7)) ([fb3f764](https://github.com/compliance-framework/workflows/commit/fb3f764aaedf8ec72c8f141bcf238567cf46a96b))
* manifest and Go module skeleton ([#3](https://github.com/compliance-framework/workflows/issues/3)) ([463e845](https://github.com/compliance-framework/workflows/commit/463e84521a134275ffe55ed284b4f81f10433db9))
* **notify:** incident state and Slack thread transitions ([dff4c3b](https://github.com/compliance-framework/workflows/commit/dff4c3b484a2f3d3f0ea0ab08d5adcdde2db0c81))
* **notify:** Slack incident threads for CI failures and recoveries ([a88510d](https://github.com/compliance-framework/workflows/commit/a88510d0bfdf72166ae41bcf821120919206d254))
* **notify:** Slack incident threads for CI failures and recoveries ([0d9d2ee](https://github.com/compliance-framework/workflows/commit/0d9d2ee7d23767525f2604a241a3b2a6a748a697))
* **plugin-probe:** CLI and policy bundle checks ([d89906b](https://github.com/compliance-framework/workflows/commit/d89906b8f7eea05054e645c8c1ae0ade11e766b3))
* **plugin-probe:** load plugins and report their protocol ([d19900c](https://github.com/compliance-framework/workflows/commit/d19900c6015893f3622b8cf17a062871b8f709f2))
* **plugin-probe:** reusable plugin-probe workflow and docs ([a2afd0a](https://github.com/compliance-framework/workflows/commit/a2afd0adfb2e98a3edadbe77536142f058c23576))
* **plugin-probe:** reusable plugin-probe workflow and docs ([4a227ed](https://github.com/compliance-framework/workflows/commit/4a227edac9d84e75f8fd1d262707c374d7390b31))
* **release:** helm chart and action release workflows ([#12](https://github.com/compliance-framework/workflows/issues/12)) ([e4ec2cd](https://github.com/compliance-framework/workflows/commit/e4ec2cdbed27f815938cd2cdd02b4906466c4ff3))
* **release:** multi-arch images and the image/ui release workflows ([#10](https://github.com/compliance-framework/workflows/issues/10)) ([2724220](https://github.com/compliance-framework/workflows/commit/2724220e4d5b670f819991b0a946ac48307463dd))
* **release:** plugin and policy release workflows and OCI previews ([#11](https://github.com/compliance-framework/workflows/issues/11)) ([f325bcb](https://github.com/compliance-framework/workflows/commit/f325bcb383f79f93866e3bed57a71bcf5b7b7cd1))
* **release:** preview images and release-candidate prereleases ([#9](https://github.com/compliance-framework/workflows/issues/9)) ([6265561](https://github.com/compliance-framework/workflows/commit/62655614b6488594f5df265743d8a73ab4027771))
* **release:** release workflow for Go libraries (release-go-lib) ([#13](https://github.com/compliance-framework/workflows/issues/13)) ([926fbd2](https://github.com/compliance-framework/workflows/commit/926fbd20322d2cbbc3bf2b419955ac2e882acf3b))
* **release:** release workflows itself with release-please ([2ddffe3](https://github.com/compliance-framework/workflows/commit/2ddffe396a93b4cb3010da7fdbb4e9c97d300943))
* **release:** release-please and release checks ([#8](https://github.com/compliance-framework/workflows/issues/8)) ([190b624](https://github.com/compliance-framework/workflows/commit/190b624a8a0164cce49f6e4b5b9a04a4fec0650e))
* **renovate:** shared Renovate preset and self-hosted runner workflow ([112235a](https://github.com/compliance-framework/workflows/commit/112235a53ac2501902be7deafba4b1e136bde91a))
* **renovate:** shared Renovate preset and self-hosted runner workflow ([c15fb79](https://github.com/compliance-framework/workflows/commit/c15fb7986408c89f7115357d4386edd80a993cbe))
* **repo-settings:** repo-settings CLI and workflow_dispatch wrapper ([680ac8c](https://github.com/compliance-framework/workflows/commit/680ac8c9d289dd6337ae8e6879e0d59b147d2360))
* **repo-settings:** sync repo merge settings and rulesets from the manifest ([#14](https://github.com/compliance-framework/workflows/issues/14)) ([c30dd42](https://github.com/compliance-framework/workflows/commit/c30dd42906306f99f0d3269d9211c2f75fe2e7e1))
* **smoke:** stack smoke test for api, ui and agent ([3c2e9ea](https://github.com/compliance-framework/workflows/commit/3c2e9ea5169aa3cb11039282712b2e423bea2dec))
* **smoke:** stack smoke test for api, ui and agent ([babbafc](https://github.com/compliance-framework/workflows/commit/babbafc81f0e6f48037a7c6b8759e2d281e2180c))
* **train:** start, comment commands, the digest draft and the dry run ([93dbe67](https://github.com/compliance-framework/workflows/commit/93dbe67760a072ab89cfa240eab31e92b8452d2d))
* **train:** the GitHub REST client, Slack threads and the train CLI ([f4943e0](https://github.com/compliance-framework/workflows/commit/f4943e0349cd0816bff6c9b81943c9f88cb0ba3d))
* **train:** the GitHub, Slack and ccf-bump interfaces and the checks rule ([ad46693](https://github.com/compliance-framework/workflows/commit/ad46693172ff57af03e1a6e4c344b12c046c58d0))
* **train:** the state machine that moves each repo through its release, stage by stage ([cd6b3f3](https://github.com/compliance-framework/workflows/commit/cd6b3f3375c45a3725dbb1461cf3fb81eb5f066f))
* **train:** the train workflow and docs ([09078c3](https://github.com/compliance-framework/workflows/commit/09078c374b1a7beaeaa77f9a4222a4a061e8ba9e))
* **train:** the train workflow, --watch, and docs/train.md ([4c6dd36](https://github.com/compliance-framework/workflows/commit/4c6dd36f077b39c44f7b791bc5a2beb9f2ed0957))
* **train:** train state, tracking issue codec, commands and digest ([3bdbfb7](https://github.com/compliance-framework/workflows/commit/3bdbfb7bf1ac73210e198154892723582c7678da))
* **vuln-summary:** weekly Dependabot alert summary to Slack ([0dd7571](https://github.com/compliance-framework/workflows/commit/0dd75715e4c6d6640c859c9ff7820a0c9411baae))
* **vuln-summary:** weekly Dependabot alert summary to Slack ([399104c](https://github.com/compliance-framework/workflows/commit/399104c31e5d51077cde44a7da1ee06a671c742b))


### Bug Fixes

* **ccf-bump:** move only the job whose pin was planned when two share a SHA ([00e5a23](https://github.com/compliance-framework/workflows/commit/00e5a23d6996d0f97ec30a3f4bac77b65e942f47))
* **ccf-bump:** warn when auto-merge can't be enabled; close superseded bump PRs ([5524c0b](https://github.com/compliance-framework/workflows/commit/5524c0b14554cc95876c9063eeceba7584ff4591))
* **ccf-bump:** warn when auto-merge can't be enabled; close superseded bump PRs ([77e6881](https://github.com/compliance-framework/workflows/commit/77e6881e7de93a343dd06cdd97f2b36090d8c2c9))
* **plugin-probe:** run local plugins by absolute path ([8c7fa37](https://github.com/compliance-framework/workflows/commit/8c7fa3736089fac8f32f43338d55eb3330483b13))
* **renovate:** merge as the release bot instead of GitHub auto-merge; RENOVATE_LIVE for scheduled runs ([a799227](https://github.com/compliance-framework/workflows/commit/a7992277ade01d80a9c091931aa008565d45ed6f))
* **renovate:** merge as the release bot instead of GitHub auto-merge; RENOVATE_LIVE for scheduled runs ([58f737a](https://github.com/compliance-framework/workflows/commit/58f737a3abdd04bf07b0ac8bf9f8e781ad109549))
* **renovate:** rebase only on conflicts so every green PR merges in one run ([d2a86e5](https://github.com/compliance-framework/workflows/commit/d2a86e5e7307ef24a577e18947c15fe8bea86642))
* **renovate:** rebase only on conflicts so every green PR merges in one run ([bef919d](https://github.com/compliance-framework/workflows/commit/bef919d1d3f2d937196842210466bb721e4e2fa1))
* **repo-settings:** non-vacuous unknown test; idempotency is checked by a second apply ([5e74982](https://github.com/compliance-framework/workflows/commit/5e74982cad2df452a38e3e235554b2fda6c5cc6e))
* **repo-settings:** report hidden ruleset bypass actors as unknown ([90f3530](https://github.com/compliance-framework/workflows/commit/90f3530fdde387fb56406326d719f9d7c71b512b))
* **repo-settings:** report hidden ruleset bypass actors as unknown ([c835300](https://github.com/compliance-framework/workflows/commit/c83530052b333d5681c31254f7e45bf95d917ca6))
* **repo-settings:** respect org-enforced security configurations and keep applying other settings ([52f1863](https://github.com/compliance-framework/workflows/commit/52f1863312a61c78afce4d25787c5275d5a8268f))
* **repo-settings:** respect org-enforced security configurations and keep applying other settings ([a444081](https://github.com/compliance-framework/workflows/commit/a444081a843e9eea828e34065eba623c8d0d61b2))
* **repo-settings:** treat merge settings a read-only token can't see as unknown ([0bf3954](https://github.com/compliance-framework/workflows/commit/0bf39542ef550f900012dd8ae0b5aa768219a20c))
* **repo-settings:** treat merge settings a read-only token can't see as unknown ([882170e](https://github.com/compliance-framework/workflows/commit/882170e1ddc9c23f43aa3ecd20b118f8920afa5c))
* **smoke:** validate numeric settings, warn on failed teardown, test the happy path ([ffa9d05](https://github.com/compliance-framework/workflows/commit/ffa9d0537580caa873b3e5708312132405014fa2))
* yaml format ([3e53856](https://github.com/compliance-framework/workflows/commit/3e538563871cd20c867fc7d05d2e4ee0e1fbe6d8))
