# Changelog

## [0.4.12](https://github.com/anatolykoptev/go-linkedin/compare/v0.4.11...v0.4.12) (2026-07-22)


### Fixed

* rewrite job-search to the 2026 Voyager Rest.li query (q=jobSearch + query tuple + geoId + decorationId-220); resolve location-&gt;geoId; parse normalized included[] JobPosting entities (was 400) ([#46](https://github.com/anatolykoptev/go-linkedin/issues/46)) ([bd31e95](https://github.com/anatolykoptev/go-linkedin/commit/bd31e9522323f3d6f13b1a4874877f434394aa09))

## [0.4.11](https://github.com/anatolykoptev/go-linkedin/compare/v0.4.10...v0.4.11) (2026-07-22)


### Added

* CDP in-page-fetch Voyager transport via go-wowa (fixes CF 302 loop) ([#43](https://github.com/anatolykoptev/go-linkedin/issues/43)) ([3411ec0](https://github.com/anatolykoptev/go-linkedin/commit/3411ec0fa73a02e6600f4ce9b908db746f89615c))

## [0.4.10](https://github.com/anatolykoptev/go-linkedin/compare/v0.4.9...v0.4.10) (2026-07-22)


### Fixed

* **client:** honour context cancellation during jitter sleep ([a1a308c](https://github.com/anatolykoptev/go-linkedin/commit/a1a308ced91b7cd8eef84ca98fa7521172353216))
* **client:** remove hardcoded clientVersion fallback, make it configurable ([1767095](https://github.com/anatolykoptev/go-linkedin/commit/176709581bbfd6d361ffbaf70f7baf9bbe0dba45))
* **client:** return error on empty CSRF token when JSESSIONID missing ([71ecd33](https://github.com/anatolykoptev/go-linkedin/commit/71ecd33756c3e3da94c133f0550c86e30c3cf09d))
* protect Client.cookies with sync.RWMutex to prevent concurrent map access ([2147944](https://github.com/anatolykoptev/go-linkedin/commit/2147944a645c4785380ea9c1be5f97af4c832130))
* return error instead of silent fallback when profile URN not found ([e16fe4c](https://github.com/anatolykoptev/go-linkedin/commit/e16fe4c377f95c1cca8fc77a0fbcd445e1b9d13a))

## [0.4.9](https://github.com/anatolykoptev/go-linkedin/compare/v0.4.8...v0.4.9) (2026-07-22)


### Added

* return typed Voyager errors (VoyagerStatusError / VoyagerHTMLResponseError) for errors.As classification ([#7](https://github.com/anatolykoptev/go-linkedin/issues/7)) ([3eb8588](https://github.com/anatolykoptev/go-linkedin/commit/3eb85889197da9424ec64beeaec3dbfb7fcf7b25))

## [0.4.8](https://github.com/anatolykoptev/go-linkedin/compare/v0.4.7...v0.4.8) (2026-07-21)


### Added

* add GetJobDetail for Voyager job-detail enrichment (go-job [#293](https://github.com/anatolykoptev/go-linkedin/issues/293)) ([#2](https://github.com/anatolykoptev/go-linkedin/issues/2)) ([7de037d](https://github.com/anatolykoptev/go-linkedin/commit/7de037da3a0555b6a08ba71a10b104831a9480b0))
* detect HTML authwall on 200 response + testBaseURL hook ([9e2b0dd](https://github.com/anatolykoptev/go-linkedin/commit/9e2b0ddd6681894b3804d519b106e4320a5f774d))


### Dependencies

* bump go-stealth to v1.19.1 ([#1](https://github.com/anatolykoptev/go-linkedin/issues/1)) ([3db9edb](https://github.com/anatolykoptev/go-linkedin/commit/3db9edb8bee3adce9127cb9f3297665be0d4a556))
