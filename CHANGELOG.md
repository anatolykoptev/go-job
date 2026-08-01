# Changelog

All notable changes to go_job are documented here.

## [1.18.1](https://github.com/anatolykoptev/go-job/compare/v1.18.0...v1.18.1) (2026-08-01)


### Bug Fixes

* **jobs:** detect and count unparseable LLM responses, never surface raw output ([#434](https://github.com/anatolykoptev/go-job/issues/434)) ([c302fc2](https://github.com/anatolykoptev/go-job/commit/c302fc2beefa497e762ca05d893cb77490edb341))

## [1.18.0](https://github.com/anatolykoptev/go-job/compare/v1.17.1...v1.18.0) (2026-08-01)


### Features

* **jobs:** emit structured JobListing from ATS sources instead of re-deriving it with an LLM ([#418](https://github.com/anatolykoptev/go-job/issues/418)) ([d039007](https://github.com/anatolykoptev/go-job/commit/d0390072adf8b3447ac0b7767017521e4bf7f279))

## [1.17.1](https://github.com/anatolykoptev/go-job/compare/v1.17.0...v1.17.1) (2026-07-31)


### Bug Fixes

* **resume_generate:** emit the approved resume document shape ([#420](https://github.com/anatolykoptev/go-job/issues/420)) ([c6b88e2](https://github.com/anatolykoptev/go-job/commit/c6b88e220fd680117087356a25838c6d869a0369))

## [1.17.0](https://github.com/anatolykoptev/go-job/compare/v1.16.0...v1.17.0) (2026-07-31)


### Features

* **pdfrender:** apply the design review, and record the standard in DESIGN.md ([#411](https://github.com/anatolykoptev/go-job/issues/411)) ([8e415b6](https://github.com/anatolykoptev/go-job/commit/8e415b6acb107bdb0e50f609c7f7cc7a2e49d110))

## [1.16.0](https://github.com/anatolykoptev/go-job/compare/v1.15.2...v1.16.0) (2026-07-31)


### Features

* **job_search:** relevance scoring, honest degradation, and a gate that ships disabled ([#404](https://github.com/anatolykoptev/go-job/issues/404)) ([4ce5eca](https://github.com/anatolykoptev/go-job/commit/4ce5eca40908165e143e0b5682c4d7f240ee0ed0))


### Bug Fixes

* **pdfrender:** name the weight the image actually has ([#403](https://github.com/anatolykoptev/go-job/issues/403)) ([f7f111f](https://github.com/anatolykoptev/go-job/commit/f7f111f86ffff732b26d06bd1e7b47a168342ebd))

## [1.15.2](https://github.com/anatolykoptev/go-job/compare/v1.15.1...v1.15.2) (2026-07-31)


### Bug Fixes

* **pdfrender:** render resumes in the font the theme names, and own the approved template ([#397](https://github.com/anatolykoptev/go-job/issues/397)) ([46d472a](https://github.com/anatolykoptev/go-job/commit/46d472a5eabd18bd00d30a10851e6b00da5dd7e3))

## [1.15.1](https://github.com/anatolykoptev/go-job/compare/v1.15.0...v1.15.1) (2026-07-31)


### Bug Fixes

* **craigslist:** default empty location to operator profile then config ([#375](https://github.com/anatolykoptev/go-job/issues/375)) ([6c7dac7](https://github.com/anatolykoptev/go-job/commit/6c7dac70910b74eafe099d8c39a267284886b3c6))

## [1.15.0](https://github.com/anatolykoptev/go-job/compare/v1.14.3...v1.15.0) (2026-07-31)


### Features

* **observability:** per-source freshness gauge + outcome counter for scheduled ingest ([#376](https://github.com/anatolykoptev/go-job/issues/376)) ([9e141ae](https://github.com/anatolykoptev/go-job/commit/9e141ae2bbd84b23232e1321a8de6e6ed58cfde4))


### Bug Fixes

* **ingest:** hackerone truncation, himalayas decode, dead algora source ([#374](https://github.com/anatolykoptev/go-job/issues/374)) ([297e8a9](https://github.com/anatolykoptev/go-job/commit/297e8a99c1e6452da903b3dfb294b180285458c2))
* **resume:** make master_resume_build incapable of damaging the profile ([#366](https://github.com/anatolykoptev/go-job/issues/366)) ([3b210d3](https://github.com/anatolykoptev/go-job/commit/3b210d3179f62ad235b70c2246a648d6b2489eef))

## [1.14.3](https://github.com/anatolykoptev/go-job/compare/v1.14.2...v1.14.3) (2026-07-30)


### Bug Fixes

* **resume:** FTS fallback on empty vector result + profile-derived vector sync ([#355](https://github.com/anatolykoptev/go-job/issues/355)) ([ddf7a20](https://github.com/anatolykoptev/go-job/commit/ddf7a20f5a9367cdb21dc05b23d7f816bb62d2a6))

## [1.14.2](https://github.com/anatolykoptev/go-job/compare/v1.14.1...v1.14.2) (2026-07-29)


### Bug Fixes

* **craigslist:** ox-browser /fetch transport, single parser, reachable blocked + genuine-empty ([#346](https://github.com/anatolykoptev/go-job/issues/346)) ([8c19a47](https://github.com/anatolykoptev/go-job/commit/8c19a4773e3af041e323fb79938224c1824b3ef8))
* **job_search:** report per-source outcome + return partial on deadline ([#340](https://github.com/anatolykoptev/go-job/issues/340)) ([c6e2a71](https://github.com/anatolykoptev/go-job/commit/c6e2a71b27662f14a9fe3e077a13fe46cce6eca6))

## [1.14.1](https://github.com/anatolykoptev/go-job/compare/v1.14.0...v1.14.1) (2026-07-25)


### Bug Fixes

* stop latching hunt_scoring_degraded on budget exhaustion ([#324](https://github.com/anatolykoptev/go-job/issues/324)) ([87b805a](https://github.com/anatolykoptev/go-job/commit/87b805a38fd9d78cccf43815f4d7faf069713f7f))

## [1.14.0](https://github.com/anatolykoptev/go-job/compare/v1.13.0...v1.14.0) (2026-07-22)


### Features

* wire opt-in LinkedIn job-search enrichment (enrich/enrich_limit passthrough, default-off; bump go-linkedin v0.4.13) ([#322](https://github.com/anatolykoptev/go-job/issues/322)) ([933c5a7](https://github.com/anatolykoptev/go-job/commit/933c5a77073cce72667e2c3f106c9829ff04ab1c))

## [1.13.0](https://github.com/anatolykoptev/go-job/compare/v1.12.0...v1.13.0) (2026-07-22)


### Features

* wire go-linkedin CDP Voyager transport behind LINKEDIN_TRANSPORT flag (default stealth); bump go-linkedin v0.4.11 ([#320](https://github.com/anatolykoptev/go-job/issues/320)) ([134118e](https://github.com/anatolykoptev/go-job/commit/134118e5bf4dd2eec1c1e46ebc75bf8ca6b3a99c))


### Bug Fixes

* thread real upstream status through Tier-A LinkedIn fetch so 429 alerts as rate-limited not network-down ([#307](https://github.com/anatolykoptev/go-job/issues/307)) ([#318](https://github.com/anatolykoptev/go-job/issues/318)) ([6ce206c](https://github.com/anatolykoptev/go-job/commit/6ce206c0e2bc39cb78b8c00d19c20a79bbff6abe))

## [1.12.0](https://github.com/anatolykoptev/go-job/compare/v1.11.0...v1.12.0) (2026-07-21)


### Features

* cap LinkedIn guest pagination at ~1000 ceiling + jittered per-page backoff ([#292](https://github.com/anatolykoptev/go-job/issues/292)) ([#300](https://github.com/anatolykoptev/go-job/issues/300)) ([19dad34](https://github.com/anatolykoptev/go-job/commit/19dad34aab310684ce6aa35c3e737da1aaeed04a))
* LinkedIn block classifier + guest-path API→browser fallback cascade ([#290](https://github.com/anatolykoptev/go-job/issues/290), [#291](https://github.com/anatolykoptev/go-job/issues/291)) ([#297](https://github.com/anatolykoptev/go-job/issues/297)) ([1530b21](https://github.com/anatolykoptev/go-job/commit/1530b216aa6cfe6e4a417fedbfc3143c76b4846c))
* plumb Easy-Apply fields onto LinkedInJob for Voyager detail path ([#294](https://github.com/anatolykoptev/go-job/issues/294)) ([#301](https://github.com/anatolykoptev/go-job/issues/301)) ([b6904e4](https://github.com/anatolykoptev/go-job/commit/b6904e4c0fa711f7c27347edb97f83ce70d00a83))
* VoyagerJobDetail wrapper + job-detail enrichment fields ([#293](https://github.com/anatolykoptev/go-job/issues/293)) ([#302](https://github.com/anatolykoptev/go-job/issues/302)) ([baad389](https://github.com/anatolykoptev/go-job/commit/baad3898a49d7b973b1c55251f73b87a9a742eb2))


### Bug Fixes

* SSE + tool keepalive via go-mcpserver v0.17.0 ([#287](https://github.com/anatolykoptev/go-job/issues/287)) ([fecd038](https://github.com/anatolykoptev/go-job/commit/fecd0381107bbfbfbf1435e6468b3e9087f45f48))

## [1.11.0](https://github.com/anatolykoptev/go-job/compare/v1.10.4...v1.11.0) (2026-07-19)


### Features

* **mcp:** adopt NewServer + KeepAlive + SchemaCache + wire go-panel mcp ([#285](https://github.com/anatolykoptev/go-job/issues/285)) ([5b4225d](https://github.com/anatolykoptev/go-job/commit/5b4225d50ba323cfbacd91d0fe99e67a2ef8a328))
* **proxy:** Tor fallback when Webshare absent/fails ([#280](https://github.com/anatolykoptev/go-job/issues/280)) ([5671941](https://github.com/anatolykoptev/go-job/commit/5671941a533762818d089c61bc62b02ff9be9e4e))


### Bug Fixes

* **ats:** per-fetch timeout context for greenhouse, lever, ashby (BH-13) ([#272](https://github.com/anatolykoptev/go-job/issues/272)) ([6fe2d58](https://github.com/anatolykoptev/go-job/commit/6fe2d5879c12912c8c6ac060e4211b74225c05e2))
* bump go-panel v0.21.0 → v0.21.1 (jsonschema tag panic fix) ([#286](https://github.com/anatolykoptev/go-job/issues/286)) ([6e29b0d](https://github.com/anatolykoptev/go-job/commit/6e29b0ddaf499fbe95e5ca6852f6066c8a15d759))
* cancelable contexts for slugcache warmup, oversize purge, ats discovery (BH-6) ([#267](https://github.com/anatolykoptev/go-job/issues/267)) ([db12b85](https://github.com/anatolykoptev/go-job/commit/db12b851c4a6e0fdcf4924f5f7b449e55c5efd8d))
* **db:** increase default pool to 25 + add pool stats gauges (BH-3) ([#266](https://github.com/anatolykoptev/go-job/issues/266)) ([0e2eef5](https://github.com/anatolykoptev/go-job/commit/0e2eef59c0e2e6559954d3bb98c0bc6070a6e5c4))
* **hunt:** schema version tracking for code/schema drift detection (BH-8) ([#273](https://github.com/anatolykoptev/go-job/issues/273)) ([7d3f5f5](https://github.com/anatolykoptev/go-job/commit/7d3f5f581c2288fb42fd8dc9c16943d3917a472d))
* **hunt:** validate OrderBy against allowlist to prevent SQL injection (BH-5) ([#269](https://github.com/anatolykoptev/go-job/issues/269)) ([41a6b49](https://github.com/anatolykoptev/go-job/commit/41a6b496a6d55a940564a4b2d45ec7c09b3bf9a2))
* **job_search:** bound connector goroutine fan-out with semaphore cap 8 (BH-1) ([#263](https://github.com/anatolykoptev/go-job/issues/263)) ([457270a](https://github.com/anatolykoptev/go-job/commit/457270af52baa863d2315a84e0a36da714834f8c))
* **mcp:** wire BearerAuth when MCP_BEARER_TOKEN is set (BH-2) ([#264](https://github.com/anatolykoptev/go-job/issues/264)) ([981f6be](https://github.com/anatolykoptev/go-job/commit/981f6bea929a4de43a43d819f53b2c9928af8ec3))
* **metrics:** remove dead metrics — gitingest, direct_ddg, direct_startpage (BH-14) ([#274](https://github.com/anatolykoptev/go-job/issues/274)) ([fe1ebd3](https://github.com/anatolykoptev/go-job/commit/fe1ebd3b985f606ad008cce29f65bb02338a63de))
* **obs:** add LLM latency, purge, enrich skip, admin UI metrics (OBS-6) ([#276](https://github.com/anatolykoptev/go-job/issues/276)) ([2fc29fd](https://github.com/anatolykoptev/go-job/commit/2fc29fd1b375124c33d8cf641d487b04c242b791))
* **obs:** add LLM latency, purge, enrich skip, admin UI metrics (OBS-6) ([#276](https://github.com/anatolykoptev/go-job/issues/276)) ([#277](https://github.com/anatolykoptev/go-job/issues/277)) ([d79cfaa](https://github.com/anatolykoptev/go-job/commit/d79cfaa3da5b977ffdec77092d26351809ccbff0))
* **oversize:** soft delete to prevent purge racing with active reads (BH-9) ([#270](https://github.com/anatolykoptev/go-job/issues/270)) ([bec0aed](https://github.com/anatolykoptev/go-job/commit/bec0aed34db6409e45df18d88c4a54455005a657))
* **search:** route web search through go-search instead of direct DDG ([#278](https://github.com/anatolykoptev/go-job/issues/278)) ([e7eaa6f](https://github.com/anatolykoptev/go-job/commit/e7eaa6f7eb41e450b1e2d73c4fff5609e15bd163))
* **slugcache:** validate Redis connectivity at init + L2 active metric (BH-12) ([#271](https://github.com/anatolykoptev/go-job/issues/271)) ([27b845f](https://github.com/anatolykoptev/go-job/commit/27b845f8e3bf95c0fd0080a66deea3661390d4af))
* **test:** document perSourceTimeout mutation is not parallel-safe (BH-16) ([#275](https://github.com/anatolykoptev/go-job/issues/275)) ([1d694be](https://github.com/anatolykoptev/go-job/commit/1d694bec085780c6a257547ff022d688966032b9))
* **worker:** atomic llmCallsThisCycle + cycle guard (BH-4, BH-7) ([#265](https://github.com/anatolykoptev/go-job/issues/265)) ([3cea0c1](https://github.com/anatolykoptev/go-job/commit/3cea0c1bc0cddd4767fa0479ac9e3067ca22f8b9))

## [1.10.4](https://github.com/anatolykoptev/go-job/compare/v1.10.3...v1.10.4) (2026-07-17)


### Bug Fixes

* **batch3:** 9 bug-hunt fixes — dead code, goroutine leak, oversize purge, breaker backoff, DB pool, fail-open default, LLM validation, ATS metric, security docs ([#243](https://github.com/anatolykoptev/go-job/issues/243)) ([e05bca8](https://github.com/anatolykoptev/go-job/commit/e05bca8d7cd9e94fcc92e6cf143db4a38f66f8f8))

## [1.10.3](https://github.com/anatolykoptev/go-job/compare/v1.10.2...v1.10.3) (2026-07-17)


### Bug Fixes

* **obs-1-4:** goroutine gauge, notifier-disabled metric, L2 write error metric, alert rules for 9 failure classes ([#241](https://github.com/anatolykoptev/go-job/issues/241)) ([60e07d9](https://github.com/anatolykoptev/go-job/commit/60e07d9317ce7bf704f1a971fac55a9fb4579945))

## [1.10.2](https://github.com/anatolykoptev/go-job/compare/v1.10.1...v1.10.2) (2026-07-17)


### Bug Fixes

* **pf-9-14:** LLM proxy key validation, LinkedIn stale-age + atomic swap, bounded L2 pool, HTTP conn pooling, fit-gate range clamp ([#239](https://github.com/anatolykoptev/go-job/issues/239)) ([b2b061a](https://github.com/anatolykoptev/go-job/commit/b2b061a5fd02351b129679017caa0509f94eb3c6))

## [1.10.1](https://github.com/anatolykoptev/go-job/compare/v1.10.0...v1.10.1) (2026-07-16)


### Bug Fixes

* **harden-before-prod:** increase scoring retries, slug cache double-checked locking, fail-fast env parsing ([#237](https://github.com/anatolykoptev/go-job/issues/237)) ([9a46b7f](https://github.com/anatolykoptev/go-job/commit/9a46b7feab19efce74245e336ba30b90dc8867e5))

## [1.10.0](https://github.com/anatolykoptev/go-job/compare/v1.9.0...v1.10.0) (2026-07-16)


### Features

* **quality:** parse salary from text, detect JD structure, add spam penalty ([#234](https://github.com/anatolykoptev/go-job/issues/234)) ([5a54f1d](https://github.com/anatolykoptev/go-job/commit/5a54f1d127e23badb1428b495cfae5d1ddb75281))

## [1.9.0](https://github.com/anatolykoptev/go-job/compare/v1.8.0...v1.9.0) (2026-07-16)


### Features

* **quality:** add deterministic job quality score (0-100, no LLM) ([#229](https://github.com/anatolykoptev/go-job/issues/229)) ([9aa5d8c](https://github.com/anatolykoptev/go-job/commit/9aa5d8c746d4d5d93be924d99a9a178f021cd620))

## [1.8.0](https://github.com/anatolykoptev/go-job/compare/v1.7.1...v1.8.0) (2026-07-16)


### Features

* **adminui:** adopt go-panel v0.20.1 MountAction for CSRF-verified POST routes ([#227](https://github.com/anatolykoptev/go-job/issues/227)) ([ecb62ac](https://github.com/anatolykoptev/go-job/commit/ecb62acc8ed8af899621d64ae77025e52228fa85))

## [1.7.1](https://github.com/anatolykoptev/go-job/compare/v1.7.0...v1.7.1) (2026-07-16)


### Bug Fixes

* **ats:** log fetch errors at WARN instead of DEBUG ([ac669c6](https://github.com/anatolykoptev/go-job/commit/ac669c6bbdce68147713929d72ff34b21265a5b2))
* **pf-2:** add total safety cap on scheduled ingest to prevent OOM ([#223](https://github.com/anatolykoptev/go-job/issues/223)) ([0c6cbcf](https://github.com/anatolykoptev/go-job/commit/0c6cbcf47a743025519d6651218e3835f970674f))
* **pf-3,pf-4:** log ERROR on empty LLM_API_KEY and DDG-without-ox-browser ([#224](https://github.com/anatolykoptev/go-job/issues/224)) ([4f38d3a](https://github.com/anatolykoptev/go-job/commit/4f38d3a083f637def92326e44b03f4b4fe9ae855))
* **pf-5:** panic-safe breaker Record via defer in ATS fetchers ([#225](https://github.com/anatolykoptev/go-job/issues/225)) ([93ea78d](https://github.com/anatolykoptev/go-job/commit/93ea78d99afa4e57633974a76ba36cca8708a07f))
* **pf6:** pass nil base to NewRedactingSlogHandler to avoid slog deadlock ([1715ad7](https://github.com/anatolykoptev/go-job/commit/1715ad733ab8250e123e587facbfece4383cb4c8))

## [1.7.0](https://github.com/anatolykoptev/go-job/compare/v1.6.0...v1.7.0) (2026-07-16)


### Features

* **esc2:** add scoring observability gauges and Prometheus alert rules ([#197](https://github.com/anatolykoptev/go-job/issues/197)) ([d0de1f4](https://github.com/anatolykoptev/go-job/commit/d0de1f40d440d61cab6a8103b7b3ac865bafcee2))
* **huntworker:** PF-2 cross-cycle LLM circuit breaker via go-kit/breaker ([#198](https://github.com/anatolykoptev/go-job/issues/198)) ([f40e7db](https://github.com/anatolykoptev/go-job/commit/f40e7db1fd9ca21f5f578cab22cb6d425155fd7a))
* **pf6:** redact Telegram bot token via transport + slog handler ([#196](https://github.com/anatolykoptev/go-job/issues/196)) ([8ca1671](https://github.com/anatolykoptev/go-job/commit/8ca167187a041ac1952228a793f0f00bd191827e))

## [1.6.0](https://github.com/anatolykoptev/go-job/compare/v1.5.0...v1.6.0) (2026-07-16)


### Features

* **adminui:** add bounties/freelance/security/audit-contests resources ([#83](https://github.com/anatolykoptev/go-job/issues/83)) ([5e7a224](https://github.com/anatolykoptev/go-job/commit/5e7a2241eb0ba498a9c05bc013f9a6d7132dfe95))
* **adminui:** add shared partials scaffold (copyBlock + charChip) ([#125](https://github.com/anatolykoptev/go-job/issues/125)) ([0a872d2](https://github.com/anatolykoptev/go-job/commit/0a872d2c4d48f606a1e71ebe0890026a6e4ca7a3))
* **adminui:** add Upwork profile page with headline + hourly_rate columns ([#110](https://github.com/anatolykoptev/go-job/issues/110)) ([576ba4c](https://github.com/anatolykoptev/go-job/commit/576ba4cb65792f750f689ec88dc4beec78d39795))
* **adminui:** adopt go-panel v0.10.0 sidebar — live hunt-count badges + working collapse ([#137](https://github.com/anatolykoptev/go-job/issues/137)) ([7c9174b](https://github.com/anatolykoptev/go-job/commit/7c9174b426fc05963c438ef67f5b789b321e7287))
* **adminui:** editable status dropdown on job detail page ([a2b5ea2](https://github.com/anatolykoptev/go-job/commit/a2b5ea2b3ee1dd30d1d103e92875f81a3315f995))
* **adminui:** editable status dropdown on job detail page ([9e6cc7d](https://github.com/anatolykoptev/go-job/commit/9e6cc7d501546f87f9e739ecb559d73bdcf271b6))
* **adminui:** filter bar on /admin/jobs (q / status / source) ([#82](https://github.com/anatolykoptev/go-job/issues/82)) ([ceb80c6](https://github.com/anatolykoptev/go-job/commit/ceb80c6b44fad020438856ee25158091e785de45))
* **adminui:** go-panel admin skeleton — jobs resource on :8896 (fail-soft) ([#80](https://github.com/anatolykoptev/go-job/issues/80)) ([c0fe9fe](https://github.com/anatolykoptev/go-job/commit/c0fe9fe63daa901daf5c036f3419c6da659da032))
* **adminui:** hunt dashboard with CachedBadge stat-cards (Phase 4a) ([#147](https://github.com/anatolykoptev/go-job/issues/147)) ([df01737](https://github.com/anatolykoptev/go-job/commit/df017376767f22644742bf037b506af074a8cc49))
* **adminui:** inline pipeline-stage dropdown in jobs table ([2c6b09e](https://github.com/anatolykoptev/go-job/commit/2c6b09e28465e121f95fd3981d7645883b14c2ca))
* **adminui:** inline pipeline-stage dropdown in jobs table ([d81be79](https://github.com/anatolykoptev/go-job/commit/d81be790015d80c9075dba216c145b4a94df1bab))
* **adminui:** job detail page with markdown + oversize resource + bespoke-route seam ([#84](https://github.com/anatolykoptev/go-job/issues/84)) ([bbb40a7](https://github.com/anatolykoptev/go-job/commit/bbb40a70fe75c1a5fcc5cd4182391d15d4baa31a))
* **adminui:** job rate (CSRF) + PDF download + recommendation display ([#85](https://github.com/anatolykoptev/go-job/issues/85)) ([dff9099](https://github.com/anatolykoptev/go-job/commit/dff9099c14486115bfaeedac4e564265f8c5a214))
* **adminui:** LinkedIn page — LINKEDIN-UPDATE.md sections + char-count chips ([#88](https://github.com/anatolykoptev/go-job/issues/88)) ([1bff0a9](https://github.com/anatolykoptev/go-job/commit/1bff0a9f39ec1a3ef2e6527a94af692356775dd2))
* **adminui:** make Shortlist the primary vacancies view ([#99](https://github.com/anatolykoptev/go-job/issues/99)) ([121d3fb](https://github.com/anatolykoptev/go-job/commit/121d3fb4cafc82a7d12d2fcbe8b60b6d34bd5ea6))
* **adminui:** move star column to index 1 (front after Title) + gold color ([f2e652e](https://github.com/anatolykoptev/go-job/commit/f2e652ef5fe2d1356086eb47c6864273bb207b84))
* **adminui:** nested resume editor via canonical ResumeDB (PR-D) ([#89](https://github.com/anatolykoptev/go-job/issues/89)) ([40ea38e](https://github.com/anatolykoptev/go-job/commit/40ea38e1d8b15aa2f3688202a9905df89041b2bc))
* **adminui:** panel shell render + read-only resume view ([#87](https://github.com/anatolykoptev/go-job/issues/87)) ([525eade](https://github.com/anatolykoptev/go-job/commit/525eade11a6b289dff7549bbcf5375fa772c7b05))
* **adminui:** per-job force re-score bypassing recency/Jaccard gates ([#98](https://github.com/anatolykoptev/go-job/issues/98)) ([248cfb1](https://github.com/anatolykoptev/go-job/commit/248cfb10960ec065765aa16942a76feb921d006d))
* **adminui:** restore curated shortlist + fix per-vacancy PDFs ([#97](https://github.com/anatolykoptev/go-job/issues/97)) ([16e898e](https://github.com/anatolykoptev/go-job/commit/16e898ef61ec459d7113750a2096016c85ba77f5))
* **adminui:** resume editor — projects, educations, certifications ([#91](https://github.com/anatolykoptev/go-job/issues/91)) ([f03babb](https://github.com/anatolykoptev/go-job/commit/f03babbe5361f045fa1ddd28e85a413fbee7e3e8))
* **adminui:** shortlist reads hunt_jobs+hunt_ratings (postgres), not _tracker.json ([#105](https://github.com/anatolykoptev/go-job/issues/105)) ([540f12b](https://github.com/anatolykoptev/go-job/commit/540f12b384765b1d86377a6d084ab1df5108e146))
* **adminui:** shortlist star toggle on jobs table ([674b5a2](https://github.com/anatolykoptev/go-job/commit/674b5a22e199668fdf35dd1beba4a0a2680e1b4c))
* **adminui:** shortlist star toggle on jobs table ([6131e6b](https://github.com/anatolykoptev/go-job/commit/6131e6b59c0ca84ee420487f4a14449b0bd1ddb8))
* **adminui:** show Scored date in job detail Overview ([#100](https://github.com/anatolykoptev/go-job/issues/100)) ([ac1bada](https://github.com/anatolykoptev/go-job/commit/ac1badac83e499083fd11a2631ab3430cbce7de0))
* **adminui:** star column to index 1 (front after Title) + gold color ([bab91b9](https://github.com/anatolykoptev/go-job/commit/bab91b9bea056ad4808aae566e834d082f47840c))
* **adminui:** two-axis fit display — Fit/Market-read chips + detail fit-card ([#86](https://github.com/anatolykoptev/go-job/issues/86)) ([2ec07da](https://github.com/anatolykoptev/go-job/commit/2ec07da1f70fe66e20fdb138c1ffa0685898ad6f))
* **adminui:** upwork editability — catalog CRUD, skill/catalog reorder, categories edit (P4a) ([#129](https://github.com/anatolykoptev/go-job/issues/129)) ([486ea09](https://github.com/anatolykoptev/go-job/commit/486ea0923b8327d0ca112a06bc6f9f25c1423d1a))
* **adminui:** Upwork profile PostgreSQL data model ([#114](https://github.com/anatolykoptev/go-job/issues/114)) ([8d2fd9d](https://github.com/anatolykoptev/go-job/commit/8d2fd9dc6bcb01a1d74146c7d323165d1eae9942))
* **applications:** PDF authority + Typst adapter + persist tool + migrator (Phases C-G) ([#115](https://github.com/anatolykoptev/go-job/issues/115)) ([026b9b4](https://github.com/anatolykoptev/go-job/commit/026b9b4883b48874091ab996a6748cead431e1ab))
* **ats:** multi-query union discovery + runtime slug cache (P1+P2) ([#65](https://github.com/anatolykoptev/go-job/issues/65)) ([6d24ef4](https://github.com/anatolykoptev/go-job/commit/6d24ef4268f9c4d10f0f4f081217432a849c48f8))
* **discovery:** branch on raw_web_search Degraded flag — fall back only on true-degraded, not clean-zero ([#148](https://github.com/anatolykoptev/go-job/issues/148)) ([503a70c](https://github.com/anatolykoptev/go-job/commit/503a70c105029411767724dab218b4a19a4f3e58))
* **discovery:** delegate ATS board URL discovery to go-search + HN fan-out fix ([#59](https://github.com/anatolykoptev/go-job/issues/59)) ([286711b](https://github.com/anatolykoptev/go-job/commit/286711bd47ead8823fec6986ab0b5522dd0e196f))
* **discovery:** raise raw_web_search ctx above server timeout + distinct degraded-fallback metric ([#150](https://github.com/anatolykoptev/go-job/issues/150)) ([22d607b](https://github.com/anatolykoptev/go-job/commit/22d607bc97c23483c07df0e43ca67ad5dc45ca1f))
* **discovery:** send board_discovery=true to go-search for ATS slug discovery ([#67](https://github.com/anatolykoptev/go-job/issues/67)) ([a63542f](https://github.com/anatolykoptev/go-job/commit/a63542f914476f7648577b1b21ea51b2cb3ed0e0))
* **hunt/notify:** clean bounty/security format + claimed-bounty skip gate ([#108](https://github.com/anatolykoptev/go-job/issues/108)) ([aef2d54](https://github.com/anatolykoptev/go-job/commit/aef2d5470f5daeb1ef386ea500c0b36306385df2))
* **hunt:** add migration 009 — recommendation fields + fit_score index ([#75](https://github.com/anatolykoptev/go-job/issues/75)) ([d194d3d](https://github.com/anatolykoptev/go-job/commit/d194d3d46086f6a3aba194940d80f485be70beab))
* **hunt:** fit gate + fit-card notify payload (fit-scoring phase 5) ([#77](https://github.com/anatolykoptev/go-job/issues/77)) ([799e544](https://github.com/anatolykoptev/go-job/commit/799e54421913e6a142ba48528dacf0412ca74ad4))
* **hunt:** hybrid Jaccard→LLM fit+success scorer (fit-scoring P4) ([#76](https://github.com/anatolykoptev/go-job/issues/76)) ([e2606cc](https://github.com/anatolykoptev/go-job/commit/e2606ccb6229a348e125a176768676184a73bda2))
* **hunt:** job-score schema + Store.SetJobScore (fit-scoring phase 1) ([#72](https://github.com/anatolykoptev/go-job/issues/72)) ([5d5466e](https://github.com/anatolykoptev/go-job/commit/5d5466e0ab30928c8571672868d90b80c50df8b4))
* **hunt:** observability + score-the-unscored sweep (fit-scoring phase 6) ([#79](https://github.com/anatolykoptev/go-job/issues/79)) ([11e815d](https://github.com/anatolykoptev/go-job/commit/11e815d01677266772dc2606529cb7e428ab388f))
* **hunt:** recency-gate Telegram notifications — push only fresh postings (HUNT_NOTIFY_MAX_AGE, default 48h) ([#70](https://github.com/anatolykoptev/go-job/issues/70)) ([a30511b](https://github.com/anatolykoptev/go-job/commit/a30511bc9de355414032dcb5b18054bbe7e822d0))
* **hunt:** scheduled opportunity ingest + selective Telegram policy ([#140](https://github.com/anatolykoptev/go-job/issues/140)) ([c2e5430](https://github.com/anatolykoptev/go-job/commit/c2e5430277ebdb5eae56c1c746c6be344e65656a))
* **hunt:** ScoringProfile loader from resume_profile + env (fit-scoring phase 2) ([#73](https://github.com/anatolykoptev/go-job/issues/73)) ([2a52b7d](https://github.com/anatolykoptev/go-job/commit/2a52b7d44f3dcc80ab9b461fdd79211a8c32bc28))
* **hunt:** split hunt_ratings.stage into triage + pipeline axes (migration 012) ([30db8a2](https://github.com/anatolykoptev/go-job/commit/30db8a26b912b86d7ea48978dce1ec00397f4e53))
* **hunt:** split hunt_ratings.stage into two independent axes (migration 012) ([920e484](https://github.com/anatolykoptev/go-job/commit/920e4840998c3cb7464b174fafd8046a6b1f0769))
* **observability:** P3 per-source outcome enrichment + duration histogram + runbook ([#61](https://github.com/anatolykoptev/go-job/issues/61)) ([70fd75e](https://github.com/anatolykoptev/go-job/commit/70fd75e1465e4ccb7f6985cb4c6681cfbb829b27))
* **pdfrender:** compact US-Letter 'resume' Typst theme (1-page resumes) ([a91bdf9](https://github.com/anatolykoptev/go-job/commit/a91bdf9ea824ea1ae3b212f66b73b17d63e37643))
* **pdfrender:** switch resume render to the new compact 'resume' theme ([080adc5](https://github.com/anatolykoptev/go-job/commit/080adc556c91ff4f913915e272abeb97aaa6200e))
* render Upwork paste blocks via shared copyBlock partial ([#127](https://github.com/anatolykoptev/go-job/issues/127)) ([42e3f5e](https://github.com/anatolykoptev/go-job/commit/42e3f5e2bf245717250d61a70048ea462175b156))
* **resume:** resume_memory on postgres+pgvector with FTS fallback (P5 Phase A pilot) ([#111](https://github.com/anatolykoptev/go-job/issues/111)) ([fe0e7b5](https://github.com/anatolykoptev/go-job/commit/fe0e7b5ca43f5671ccb1f501f8e327aa2fa80221))
* **tracker:** job_tracker MCP tools use postgres, retire SQLite tracker.db ([#107](https://github.com/anatolykoptev/go-job/issues/107)) ([247c797](https://github.com/anatolykoptev/go-job/commit/247c79783f4d04f6fca0b2b77365ad8f850170ca))
* **tracker:** one-shot migrate _tracker.json favorites into hunt_jobs+hunt_ratings ([#109](https://github.com/anatolykoptev/go-job/issues/109)) ([66d37a3](https://github.com/anatolykoptev/go-job/commit/66d37a3a3cabe925aa223f2faa3b3126a02af65d))
* **vacancy-ingest:** add vacancy_ingest MCP tool + persist-disabled metrics ([#103](https://github.com/anatolykoptev/go-job/issues/103)) ([09aef35](https://github.com/anatolykoptev/go-job/commit/09aef351a560ce89cb72b3a83d647040d63cb86c))


### Bug Fixes

* **adminui:** add required to rate-form stage select; lock escaping test ([09d4ac8](https://github.com/anatolykoptev/go-job/commit/09d4ac829ac6aefd7c2f4ab2f7aec8efba1c3595))
* **adminui:** address review-council findings on status dropdown ([6dd500b](https://github.com/anatolykoptev/go-job/commit/6dd500b1ee15aae18ea5b3a9936e5e37cf9a2fbf))
* **adminui:** bind admin listener to all interfaces inside container ([#81](https://github.com/anatolykoptev/go-job/issues/81)) ([82a2f8b](https://github.com/anatolykoptev/go-job/commit/82a2f8bb159c7e2f7e78ea7629eae8339d342231))
* **adminui:** remove stray '&gt;' in star form tag + regression guard ([5a8f26e](https://github.com/anatolykoptev/go-job/commit/5a8f26e6da9f94296d05674ea5f751fa59d7a608))
* **adminui:** repair list pagination, extract ATS company, add Docs column to jobs ([#130](https://github.com/anatolykoptev/go-job/issues/130)) ([f6bf79d](https://github.com/anatolykoptev/go-job/commit/f6bf79d9b5cfa9e675bffa69c635d030216c54b1))
* **adminui:** reroute shortlist star through hunt_ratings, drop boolean ([4a0e891](https://github.com/anatolykoptev/go-job/commit/4a0e891721ea6d05b357c3771cb497c1b6c4ef74))
* **adminui:** reroute shortlist star through hunt_ratings, drop boolean ([2c17ef9](https://github.com/anatolykoptev/go-job/commit/2c17ef9d01ae7e782d0557a7a4cdfc169f136c9e))
* **adminui:** restore resume/cover download links on shortlist Docs cell ([#112](https://github.com/anatolykoptev/go-job/issues/112)) ([d016ba0](https://github.com/anatolykoptev/go-job/commit/d016ba0a9bb439e232699d4c7d1b9eb71c95c2fb))
* **adminui:** split Company into its own sortable column (jobs + shortlist) ([#126](https://github.com/anatolykoptev/go-job/issues/126)) ([229d0a0](https://github.com/anatolykoptev/go-job/commit/229d0a0a0604b827bc1dd7e7e79247e470d1b71e))
* **adminui:** stage dropdown — color tokens, enum dedup, button submit, disabled placeholder ([aecc189](https://github.com/anatolykoptev/go-job/commit/aecc1895b9d855a508928c860416c0c817a3dc63))
* **ats:** populate posted_at from ATS API dates so recency-gate works (greenhouse/lever/ashby) ([#71](https://github.com/anatolykoptev/go-job/issues/71)) ([d0fb152](https://github.com/anatolykoptev/go-job/commit/d0fb1526121e1567283eae15dee735c02217c464))
* **ats:** raise board cap to 16 MB + truncation guard + fetch-error counter ([#64](https://github.com/anatolykoptev/go-job/issues/64)) ([cecf5e1](https://github.com/anatolykoptev/go-job/commit/cecf5e1aafb2052ce54b9ff5c0488ebe73e17dd9))
* **ats:** stream-decode board fetches; raise DoS ceiling to 64 MB ([#66](https://github.com/anatolykoptev/go-job/issues/66)) ([b9842b0](https://github.com/anatolykoptev/go-job/commit/b9842b033fe14b8e2cf2cfa7f1b03ec7eae3f8c8))
* bug-hunt remediation — 11 fixes for silent downgrades, data loss, and wiring gaps ([#187](https://github.com/anatolykoptev/go-job/issues/187)) ([0df454e](https://github.com/anatolykoptev/go-job/commit/0df454e5e6c567ce7265b3f61142d271d554310f))
* **discovery:** delegate ATS board-URL discovery to raw_web_search (scrapers-only, no embedder contention) ([#144](https://github.com/anatolykoptev/go-job/issues/144)) ([8d850d8](https://github.com/anatolykoptev/go-job/commit/8d850d8e81cbf53c665374cc5ea7a8a24f07ce88))
* **discovery:** restore job discovery — ox-browser tier + HN fan-out bound + searxng masquerade suppression (P0) ([#58](https://github.com/anatolykoptev/go-job/issues/58)) ([10f0698](https://github.com/anatolykoptev/go-job/commit/10f06985d2df1a398e8469a955f992972435c7b9))
* **docker:** run go-job as non-root user (uid=1001) ([#120](https://github.com/anatolykoptev/go-job/issues/120)) ([c751970](https://github.com/anatolykoptev/go-job/commit/c751970f11567e2ac4a1bb670958c768359fc9ee))
* **docker:** run go-job non-root via USER directive + write PDFs 0644 (readable under cap_drop) ([#124](https://github.com/anatolykoptev/go-job/issues/124)) ([3620bf3](https://github.com/anatolykoptev/go-job/commit/3620bf3477ca901a5a854add120606bab76b25f8))
* **engine:** suppress twitter guest-bootstrap when go-social is configured ([#146](https://github.com/anatolykoptev/go-job/issues/146)) ([36b0471](https://github.com/anatolykoptev/go-job/commit/36b0471127daabc32f40c0b086d1c2bba1cf466c))
* **himalayas:** route through hardened proxy fetcher to bypass CF JA3 fingerprint ([#143](https://github.com/anatolykoptev/go-job/issues/143)) ([3a1cef9](https://github.com/anatolykoptev/go-job/commit/3a1cef9d1ecb407c72ba8ad1b6780aece918345f))
* **hunt_list:** replace any-typed Entries with []map[string]any to fix MCP boolean schema ([#43](https://github.com/anatolykoptev/go-job/issues/43)) ([8d24c60](https://github.com/anatolykoptev/go-job/commit/8d24c603595c5ffe4a730c76924bb8e6fa240985))
* **hunt-list:** add spill guard + description snippet ([#45](https://github.com/anatolykoptev/go-job/issues/45)) ([d2daefb](https://github.com/anatolykoptev/go-job/commit/d2daefbe54dd3a668419f6669d2ed26ab34f0d81))
* **hunt-list:** correct misleading enrichment description + add server-handled counter ([#50](https://github.com/anatolykoptev/go-job/issues/50)) ([af61ccc](https://github.com/anatolykoptev/go-job/commit/af61cccd04775837b4a824a30b71c760348517f7))
* **hunt:** address all council review findings (CRITICAL/HIGH/MEDIUM/LOW) ([fbbbae3](https://github.com/anatolykoptev/go-job/commit/fbbbae34491af48c898d91008ddf931ea21f7cd1))
* **hunt:** plug tx leak in ToggleShortlistStar + reconcile stale test ([93186bd](https://github.com/anatolykoptev/go-job/commit/93186bd5e1942072460128f088ea326f280c6bc9))
* **hunt:** send Telegram notifications via go-kit ProductSink (own bot, not vaelor loopback) ([#69](https://github.com/anatolykoptev/go-job/issues/69)) ([539299e](https://github.com/anatolykoptev/go-job/commit/539299e379bfb0042a9dead2f7eb2e550704e030))
* **hunt:** tokenize opportunity_search query so multi-word filters match ([#163](https://github.com/anatolykoptev/go-job/issues/163)) ([3326029](https://github.com/anatolykoptev/go-job/commit/33260291e3ba94a0a826c29f3070551063295d62))
* **jobs:** add ashby to LLM source enum + URL mapping (label was 'other') ([#68](https://github.com/anatolykoptev/go-job/issues/68)) ([e39cf4f](https://github.com/anatolykoptev/go-job/commit/e39cf4f0b35cab49c350526229666765eb3a656d))
* **jobs:** GetUpworkProfile loads skills+catalog when no profile row exists ([#135](https://github.com/anatolykoptev/go-job/issues/135)) ([238958e](https://github.com/anatolykoptev/go-job/commit/238958ec453e3771671b93eed7d6b6c943ece1ad))
* **mcp:** raise ReadTimeout to 5m to prevent idle connection drops ([#46](https://github.com/anatolykoptev/go-job/issues/46)) ([5465af1](https://github.com/anatolykoptev/go-job/commit/5465af1f6e2639f964187484273c30680651d0f6))
* **mcp:** return tool results as application/json, not SSE ([#55](https://github.com/anatolykoptev/go-job/issues/55)) ([890a2fd](https://github.com/anatolykoptev/go-job/commit/890a2fd6a3330115fab0b835a3253d0e3cecbd5f))
* **metrics:** pre-register platform result series at boot ([#162](https://github.com/anatolykoptev/go-job/issues/162)) ([ee1ba79](https://github.com/anatolykoptev/go-job/commit/ee1ba7977e73d9af887a338eceb1d49d0de67381))
* P4 broken sources — habr parse_fail, lever empty, hn/inspira timeout ([#62](https://github.com/anatolykoptev/go-job/issues/62)) ([c50641d](https://github.com/anatolykoptev/go-job/commit/c50641dc9ef64df96cb58c01497fcb0e16950a79))
* **pdfrender:** normalize pandoc title block before prepending liga preamble ([92b97c7](https://github.com/anatolykoptev/go-job/commit/92b97c7012530bb823461719e125d646dc6b0e22))
* **pdfrender:** normalize pandoc title block before prepending liga preamble ([aa59e49](https://github.com/anatolykoptev/go-job/commit/aa59e49601d4a752e94432386c12ccaff47105db))
* **profile:** route user profile through the writable uploads base (read-only-FS class) ([#49](https://github.com/anatolykoptev/go-job/issues/49)) ([f3eb627](https://github.com/anatolykoptev/go-job/commit/f3eb6273752b3dc23be8d4626b04c64525cfae4e))
* **research:** bound optional company-research substep in pitch_generate + interview_prep ([#52](https://github.com/anatolykoptev/go-job/issues/52)) ([498f8f1](https://github.com/anatolykoptev/go-job/commit/498f8f1dc2b41bbcab921da918cc39aecabb7d67))
* resume_profile returns 0 projects on NULL description/url scan ([7f23978](https://github.com/anatolykoptev/go-job/commit/7f239788731c71c491500c4421c7b8dabdd41a60))
* resume_profile returns 0 projects on NULL description/url scan ([e139b81](https://github.com/anatolykoptev/go-job/commit/e139b81eb19c7bec70f2855f709d64bca0f6cd23))
* **resume_profile:** de-swallow loadDomains/loadMethodologies (8/8) ([e1b19ac](https://github.com/anatolykoptev/go-job/commit/e1b19acba18859229de2deae5b8eaf520023741b))
* **resume:** drop redundant LOAD 'age' to support non-superuser DB roles ([#78](https://github.com/anatolykoptev/go-job/issues/78)) ([147e912](https://github.com/anatolykoptev/go-job/commit/147e912d786c6920a93d2b4f31ab4b5d6aa3ec3d))
* **search:** route ATS/YC/Indeed/Google job discovery off dead SearXNG to go-engine DIRECT ([#56](https://github.com/anatolykoptev/go-job/issues/56)) ([0e0a19e](https://github.com/anatolykoptev/go-job/commit/0e0a19ebea3f876f3685d01b997dd9cabb1c7fe9))
* **search:** route company-research from dead SearXNG to go-engine DIRECT ([#53](https://github.com/anatolykoptev/go-job/issues/53)) ([44ae4f9](https://github.com/anatolykoptev/go-job/commit/44ae4f9c72aef947d9d71b0136c738ae82f4fa8b))
* **search:** use DirectClient (no-proxy Chrome-TLS) for direct scrapers ([#54](https://github.com/anatolykoptev/go-job/issues/54)) ([e749853](https://github.com/anatolykoptev/go-job/commit/e749853b8cbe2deac09f3ae467bbd4cbff41bfc7))
* **shortlist:** apply review fixes — aria, CSRF helper, advanced-stage NO-OP, tests ([2a2cfcc](https://github.com/anatolykoptev/go-job/commit/2a2cfcc6732c5c4580c5e9d0deb50d9c1839100b))
* **tools:** bound company-research substep + raise per-tool timeouts for heavy LLM/IO tools ([#48](https://github.com/anatolykoptev/go-job/issues/48)) ([e9ae88e](https://github.com/anatolykoptev/go-job/commit/e9ae88e17c7c5696d4ebbe68fdef9de270543a4d))
* **upwork:** resolve HIGH/MEDIUM/LOW council findings from PR [#114](https://github.com/anatolykoptev/go-job/issues/114) ([#118](https://github.com/anatolykoptev/go-job/issues/118)) ([b93f9c7](https://github.com/anatolykoptev/go-job/commit/b93f9c7493c39cd7584501244da3400e4ca45076))

## [1.5.0](https://github.com/anatolykoptev/go-job/compare/v1.4.0...v1.5.0) (2026-06-20)


### Features

* **algora-jobs:** add Algora hiring/jobs connector ([#36](https://github.com/anatolykoptev/go-job/issues/36)) ([c661069](https://github.com/anatolykoptev/go-job/commit/c661069c3b6582444c9046d11db673e0e8c62447))

## [1.4.0](https://github.com/anatolykoptev/go-job/compare/v1.3.2...v1.4.0) (2026-06-20)


### Features

* **fetcher:** bump go-engine v1.12.1 + opt into direct-first tiered fallback ([#9](https://github.com/anatolykoptev/go-job/issues/9)) ([75e1d52](https://github.com/anatolykoptev/go-job/commit/75e1d52502d48e2df0df46521dc7362a81833b9a))
* **fetcher:** wire go-engine v1.13.0 tier router metrics ([#11](https://github.com/anatolykoptev/go-job/issues/11)) ([f927051](https://github.com/anatolykoptev/go-job/commit/f9270519b549629c7907fcf9b5e3f6af610fd974))
* **go-job:** tracker→uploads + Ashby + ATS tools + ratelimit + breaker ([#22](https://github.com/anatolykoptev/go-job/issues/22)) ([7d26d0f](https://github.com/anatolykoptev/go-job/commit/7d26d0f17fb5bf3e6558f3d287159c53c12be143))
* **hunt:** Phase 1 — domain-typed persistent tables + L1 url-hash dedup ([#17](https://github.com/anatolykoptev/go-job/issues/17)) ([fc7fa8d](https://github.com/anatolykoptev/go-job/commit/fc7fa8dbe9f319e10228cec38d47b4a5ec263d71))
* **hunt:** Phase 2 — canonical URL normalizer for cross-source dedup ([#18](https://github.com/anatolykoptev/go-job/issues/18)) ([756d412](https://github.com/anatolykoptev/go-job/commit/756d412cbb1ac1b7d72bef2925ca417090e340a1))
* **hunt:** status enrichment via lazy on-read + drop hand-rolled monitors ([#19](https://github.com/anatolykoptev/go-job/issues/19)) ([54f24f1](https://github.com/anatolykoptev/go-job/commit/54f24f179eab14e5625e64f479a361d8e69a1b99))
* Inspira (careers.un.org) + UNDP scrapers for job_search ([#25](https://github.com/anatolykoptev/go-job/issues/25)) ([5c0a7d3](https://github.com/anatolykoptev/go-job/commit/5c0a7d33fd20a076ba31bc7cf4dc5155ca4d4732))
* **jobs:** Cantina + Code4rena audit contest sources ([#13](https://github.com/anatolykoptev/go-job/issues/13)) ([d3642ff](https://github.com/anatolykoptev/go-job/commit/d3642ff03a418eb6671087fcce85cf2917f2960b))
* **jobs:** Sherlock audit contest source (+ pre-commit lint cleanup) ([#12](https://github.com/anatolykoptev/go-job/issues/12)) ([ee0273d](https://github.com/anatolykoptev/go-job/commit/ee0273dad66aad3552af2fcf90f7000ca46c288c))
* **jobs:** wire Sherlock/Cantina/Code4rena into security_monitor + Prometheus counters ([#14](https://github.com/anatolykoptev/go-job/issues/14)) ([0a679c8](https://github.com/anatolykoptev/go-job/commit/0a679c85853742c47ea56fc49f1e3f3df7fb52d9))
* **llm:** wire LLM_MODEL_FALLBACK chain (Phase 2) ([362be56](https://github.com/anatolykoptev/go-job/commit/362be56192d677ebe05ec604dcb5474809aff5c6))
* **llm:** wire LLM_MODEL_FALLBACK chain (Phase 2) ([4a42e84](https://github.com/anatolykoptev/go-job/commit/4a42e84966240a42edccbfdcef6ff283f12b3ee9))
* **oversize:** Postgres spillover for large MCP responses + go-kit v0.65.0 ([#15](https://github.com/anatolykoptev/go-job/issues/15)) ([59c9b1c](https://github.com/anatolykoptev/go-job/commit/59c9b1ca290c2fcc6acc8165577f993b17ffe472))
* send NERV_MCP_TOKEN bearer on go-nerv client ([#24](https://github.com/anatolykoptev/go-job/issues/24)) ([3cb0aa5](https://github.com/anatolykoptev/go-job/commit/3cb0aa51040aa292dd90af960c13db30af63ae51))


### Bug Fixes

* **hunt:** tag source=inspira/undp for UN scrapers ([#26](https://github.com/anatolykoptev/go-job/issues/26)) ([8b97c3c](https://github.com/anatolykoptev/go-job/commit/8b97c3cbe10b4a0f203c0584fa23bd923781faf6))
* **master_resume:** abort on clear errors, flag truncated resumes ([#6](https://github.com/anatolykoptev/go-job/issues/6)) ([6c30cf8](https://github.com/anatolykoptev/go-job/commit/6c30cf823125465246c39f3f4abc4530b319d67e))
* **memdb:** retry DeleteByUser on transient deadlock 500s ([#7](https://github.com/anatolykoptev/go-job/issues/7)) ([fc39f26](https://github.com/anatolykoptev/go-job/commit/fc39f26f6104d4ceff1ed5dabba7c51c3629177a))
* **memdb:** use bulk delete_all_memories endpoint for clear ([#8](https://github.com/anatolykoptev/go-job/issues/8)) ([f5bd60d](https://github.com/anatolykoptev/go-job/commit/f5bd60d71cebb7077b219bb47b2999f98d3e01f0))
* **test:** align TestBuildSourcesTextTruncation with go-engine v1.12 semantic ([#10](https://github.com/anatolykoptev/go-job/issues/10)) ([1b7aad2](https://github.com/anatolykoptev/go-job/commit/1b7aad28c0250576d113766cd085730ad2e24d57))

## [1.0.0] — 2026-02-20

First production release.

### New Tools (4 MCP tools total)

- **`job_search`** — LinkedIn, Greenhouse, Lever, YC, HN, Indeed, Хабр Карьера
- **`remote_work_search`** — RemoteOK, WeWorkRemotely, Remotive, SearXNG
- **`freelance_search`** — Freelancer.com (direct API), Upwork (SearXNG)
- **`job_match_score`** — Jaccard keyword scoring: resume vs job listings (0–100)

Plus 7 career tools: `resume_analyze`, `resume_tailor`, `cover_letter_generate`, `company_research`, `salary_research`, `job_tracker_add/list/update`.

### Highlights

#### job_search
- **Indeed GraphQL API** — internal iOS app endpoint, structured salary ranges, SearXNG fallback
- **LinkedIn pagination** — up to 50 results per query (was 25 max)
- **LinkedIn Easy Apply filter** — `easy_apply: true` → `f_JIYN=true`
- **LinkedIn geo_id** — 42 cities/countries map to precise LinkedIn geoId (more accurate than text location)
- **Structured salary** — `salary_min`, `salary_max`, `salary_currency`, `salary_interval` alongside human-readable `salary`
- **Canonical deduplication** — cross-source dedup by normalized job title (strips "at CompanyName", collapses punctuation)
- **Indeed + Habr wired** — were defined but not called; now proper parallel sources

#### remote_work_search
- **Remotive** — free public JSON API (`remotive.com/api/remote-jobs?search=...`), no auth required
- Now 3 direct API sources + SearXNG

#### job_match_score (new)
- Extracts keywords from resume once, scores all jobs in batch
- Jaccard similarity: `|resume ∩ job| / |resume ∪ job| × 100`
- Returns `matching_keywords` (your strengths for this role) and `missing_keywords` (skills gap)
- Tech-aware tokenizer: preserves `c++`, `c#`, `node.js`

### Architecture
- Fully standalone module (`github.com/anatolykoptev/go_job`) — no dependency on go-search
- Chrome TLS fingerprint (`bogdanfinn/tls-client`) for anti-bot bypass on LinkedIn/Indeed
- 2-tier cache: L1 in-memory + L2 Redis (graceful fallback to L1 if Redis unavailable)
- Exponential backoff retry on all HTTP calls

## [0.9.0] — 2026-02-15

- AIHawk-level career assistant: 8 new MCP tools (resume_analyze, cover_letter_generate, resume_tailor, salary_research, company_research, job_tracker_*)
- Full test suite for new tools
- Per-tool documentation in `docs/tools/`

## [0.8.0] — 2026-02-10

- Decoupled from go-search into standalone module
- Greenhouse + Lever ATS sources
- HN Who is Hiring integration (Algolia)
- YC workatastartup.com scraper
- Habr Карьера API client
- Indeed SearXNG fallback
