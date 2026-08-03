# LLM Salvage Traps (Issue #413)

Measured during #413's five review rounds. Documented here so they are not rediscovered.

## 1. Anchoring on the first `"jobs"` substring is not a parse

A model that restates the schema before answering yields the placeholder as a
job listing (`Title: "job title"`), labelled healthy. That is a **fabrication**
replacing a visible failure — strictly worse.

**Defense:** never use substring anchor as proof of parse. Require
`json.Unmarshal` success or explicit `finish_reason == "stop"`.

## 2. Mid-array decode error must not end the scan

One record with `"salary_min":"160000"` (string where `*int` is declared —
the prompt itself warns models about this) discarded 8 of 10 complete records
while reporting `dropped=1`.

**Defense:** decode elements as `json.RawMessage` first, then unmarshal each
independently — that never fails on a type mismatch and lets the scan continue.

## 3. Array closed ≠ response complete

A cut inside the enclosing object after the array closes was classified healthy
with `dropped=0` and an empty summary reaching the user.

**Defense:** must check that the root object also closes (`}` balanced) before
classifying as healthy. `finish_reason == "length"` from the API is the source
of truth.

## 4. Trailing prose is not truncation

`json.Unmarshal` rejects trailing non-whitespace, so a chat-tuned model
appending "Hope this helps!" lands in the salvage path and contaminates the
very counter built to measure the bug.

**Defense:** strip text after the first `}` at top level in the raw response
before feeding to the salvage parser.

## Summary

| Trap | Signal | Wrong conclusion | Right conclusion |
|------|--------|------------------|-----------------|
| 1. Anchoring | First `"jobs"` hit | "has data" | Check actual JSON |
| 2. Mid-array error | One type mismatch | "whole response corrupt" | Continue to next element |
| 3. Cut after array | Array closed | "complete" | Check root `}` balance + finish_reason |
| 4. Trailing prose | Unmarshal failure | "truncation" | Strip post-JSON then re-parse |