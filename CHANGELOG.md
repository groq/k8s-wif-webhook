# Changelog

## [1.1.0](https://github.com/groq/k8s-wif-webhook/compare/v1.0.0...v1.1.0) (2025-08-25)


### Features

* add multi-container and init container support for WIF injection ([#8](https://github.com/groq/k8s-wif-webhook/issues/8)) ([66b6cea](https://github.com/groq/k8s-wif-webhook/commit/66b6ceaa21505dd8a1aeca347cf8974c7659fd42))

## [1.0.0](https://github.com/groq/k8s-wif-webhook/compare/v0.5.2...v1.0.0) (2025-08-04)


### ⚠ BREAKING CHANGES

* no mutation on pod update ([#7](https://github.com/groq/k8s-wif-webhook/issues/7))

### Bug Fixes

* no mutation on pod update ([#7](https://github.com/groq/k8s-wif-webhook/issues/7)) ([5539550](https://github.com/groq/k8s-wif-webhook/commit/5539550ba014b4f79f8edcc39bfa99808f5b0d68))
* the real treasure was the defaults we had along the way ([#5](https://github.com/groq/k8s-wif-webhook/issues/5)) ([7322a03](https://github.com/groq/k8s-wif-webhook/commit/7322a03a4521b88b495eb993e2019a35776c8664))

## [0.5.2](https://github.com/groq/k8s-wif-webhook/compare/v0.5.1...v0.5.2) (2025-07-08)


### Bug Fixes

* empty pod names in admission webhook logs ([3d8e2f6](https://github.com/groq/k8s-wif-webhook/commit/3d8e2f63392522204833affb0f5b93884a83bdae))

## [0.5.1](https://github.com/groq/k8s-wif-webhook/compare/v0.5.0...v0.5.1) (2025-07-04)


### Bug Fixes

* use GROQ_GITHUB_BOT_REPO_TOKEN token for release-please ([c9cc617](https://github.com/groq/k8s-wif-webhook/commit/c9cc617eafb022d3064312aa314b6abd50c18de0))

## [0.5.0](https://github.com/groq/k8s-wif-webhook/compare/v0.4.1...v0.5.0) (2025-07-04)


### Features

* add detailed logging for skipped injection cases ([4f94b7b](https://github.com/groq/k8s-wif-webhook/commit/4f94b7b4fefac86f3c344077a60f0897b9c627da))
* add metrics for injection operations and conflicts ([3be77d1](https://github.com/groq/k8s-wif-webhook/commit/3be77d16be0958debacb70e505e3e62c7afaa10f))
* Kubernetes Workload Identity Federation admission webhook ([6118e36](https://github.com/groq/k8s-wif-webhook/commit/6118e368cb664e372f7bfac0fe3755be3f8d3fb5))
* promote conflict logs to Info level and add Kubernetes events ([ec90c73](https://github.com/groq/k8s-wif-webhook/commit/ec90c73d54cfb8c6d48891b86b5d1c5dd302e6bd))


### Bug Fixes

* add RBAC permissions for Kubernetes events ([d1bc44d](https://github.com/groq/k8s-wif-webhook/commit/d1bc44d023e6774a7f5a052aa87adb23c6cf5bd9))
* add release trigger and workflow_dispatch to build workflow ([d233ef3](https://github.com/groq/k8s-wif-webhook/commit/d233ef33ee2796c5fb426027f46646c8293ab5d2))
* prevent duplicate field injection in webhook ([a0c7774](https://github.com/groq/k8s-wif-webhook/commit/a0c77745d7651b2f40ce235ee58fe78adf340983))
* set up release-please on new repository ([85b47cb](https://github.com/groq/k8s-wif-webhook/commit/85b47cbc643b6e37adb295f6904ad7c57577489f))

## [0.4.1](https://github.com/fujin/k8s-wif-webhook/compare/v0.4.0...v0.4.1) (2025-06-22)


### Bug Fixes

* add RBAC permissions for Kubernetes events ([d1bc44d](https://github.com/fujin/k8s-wif-webhook/commit/d1bc44d023e6774a7f5a052aa87adb23c6cf5bd9))

## [0.4.0](https://github.com/fujin/k8s-wif-webhook/compare/v0.3.0...v0.4.0) (2025-06-22)


### Features

* add detailed logging for skipped injection cases ([4f94b7b](https://github.com/fujin/k8s-wif-webhook/commit/4f94b7b4fefac86f3c344077a60f0897b9c627da))
* add metrics for injection operations and conflicts ([3be77d1](https://github.com/fujin/k8s-wif-webhook/commit/3be77d16be0958debacb70e505e3e62c7afaa10f))
* promote conflict logs to Info level and add Kubernetes events ([ec90c73](https://github.com/fujin/k8s-wif-webhook/commit/ec90c73d54cfb8c6d48891b86b5d1c5dd302e6bd))


### Bug Fixes

* prevent duplicate field injection in webhook ([a0c7774](https://github.com/fujin/k8s-wif-webhook/commit/a0c77745d7651b2f40ce235ee58fe78adf340983))

## [0.3.0](https://github.com/fujin/k8s-wif-webhook/compare/v0.2.0...v0.3.0) (2025-06-18)


### Features

* Kubernetes Workload Identity Federation admission webhook ([6118e36](https://github.com/fujin/k8s-wif-webhook/commit/6118e368cb664e372f7bfac0fe3755be3f8d3fb5))


### Bug Fixes

* add release trigger and workflow_dispatch to build workflow ([d233ef3](https://github.com/fujin/k8s-wif-webhook/commit/d233ef33ee2796c5fb426027f46646c8293ab5d2))

## [0.2.0](https://github.com/fujin/k8s-wif-webhook/compare/v0.1.0...v0.2.0) (2025-06-18)


### Features

* Kubernetes Workload Identity Federation admission webhook ([6118e36](https://github.com/fujin/k8s-wif-webhook/commit/6118e368cb664e372f7bfac0fe3755be3f8d3fb5))
