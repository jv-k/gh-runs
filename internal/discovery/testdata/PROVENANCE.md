# What in these cassettes was measured, and what was composed

[local-store](../../../docs/features/local-store/requirements.md) R18 asks that a cassette replay what the API actually said. These five do not, and cannot, in whole. This file records the line between the parts taken from the wire and the parts written to exercise a requirement, so a reader knows which half they are looking at.

The distinction matters because of [#154](https://github.com/jv-k/gh-runs/issues/154). Every fixture here taped `200 OK` for an enumeration URL the API answers `422` to, across seven enumeration requests in five files. The suite stayed green for as long as the code and the fixtures agreed with each other, which is the failure mode a fixture is supposed to remove. A cassette attests to what the API said to the request we taped. It says nothing about whether that request is one the API serves.

## Composed, on purpose

**The repositories.** `jv-k/alpha` through `jv-k/epsilon`, `jv-k/local`, and the accounts in `tiers.yaml` and `exclude_reversal.yaml` do not exist. They are shaped to exercise named criteria: R7's permission tiers, an archived repository, AC1's pagination stop, and the exclude list's reversal over a warm store. A live account returns none of that, and recording one here would erase every criterion the fixtures were built for. It would also commit a real account's repository names, private ones included, to a public repository.

**The ETags, and the response header sets.** `"enum-page-1"` and its siblings are legible stand-ins. The rate-limit headers carry plausible figures rather than observed ones. Nothing asserts on their values, only on their presence and on the 304 exchanges they drive.

## Measured against api.github.com, 2026-07-27

**The enumeration request URL.** `user/repos?per_page=100&affiliation=owner,collaborator,organization_member`, with no `type`. `type` is mutually exclusive with `affiliation` and with `visibility`, and sending it alongside either is a 422 on every token and every account, because it is parameter validation:

```sh
$ gh api "user/repos?per_page=100&affiliation=owner,collaborator,organization_member&type=all"
{"message":"If you specify visibility or affiliation, you cannot specify type.","status":"422"}
$ gh api "user/repos?per_page=100&affiliation=owner,collaborator,organization_member"
200
```

**The `Link` header's shape, in `pass_basic.yaml`.** The live header percent-encodes the commas the request sent, so `rel="next"` reads `affiliation=owner%2Ccollaborator%2Corganization_member&page=2`. `enumerate` re-requests that URL verbatim, so page 2's taped request carries the encoded form and not the raw one. `rel="last"` is present alongside `rel="next"`, which the fixture also reflects. The code reads neither `rel="last"` nor `total_count` ([ADR-0005](../../../docs/adr/0005-pagination-and-total-count.md)), so its presence is recorded rather than relied on.

## The retirement fixtures, and the one thing measured about them

`retire_two_definitive.yaml`, `retire_reset_on_success.yaml`, `retire_transient_between.yaml`, `retire_ratelimited_403.yaml` and `retire_authorization_403.yaml` are **composed in whole**. Nothing in them was recorded. They tape R23's four acceptance sequences plus the authorization case, and a live account has no repository that answers 404 twice on request.

Two properties are deliberate rather than incidental.

**The probes carry no `ETag`.** A re-probe is then unconditional, so every request for one repository matches on method and URL alone and the interactions play in the order they are taped. R23 is defined over consecutive answers, and a sequence is not expressible under replayable interactions, so these five are the only fixtures here played with each interaction consumed once (`withSequentialReplay`).

**The two 403 bodies are the whole point of those two files.** They differ only in `documentation_url`, and that field is what the governor's classifier reads. `retire_authorization_403.yaml` names the reference page for the endpoint the probe requested, so the verdict is authorization and the failure counts. `retire_ratelimited_403.yaml` names a rate-limit page, which names no resource of ours, so the verdict is rate limiting and the failure counts for nothing. AC16 requires exactly this: a bare 403 classifies as rate limiting by default and would satisfy the criterion for the wrong reason.

**What is not composed here is the classifier's rule**, which was measured for [rate-governor](../../../docs/features/rate-governor/requirements.md) open question 1 and is recorded there, not re-derived in this file. These fixtures exercise that rule. They are not evidence for it.

## Before you change a request line here

Measure it. A URL edited to match the code is a fixture that is fictional about a URL that happens to work, which is what #154 found seven times. The mutual exclusion itself is held by `TestEnumeratePathDoesNotCombineTypeWithAffiliation`, which reads the production constant rather than any cassette, because that is the one assertion no fixture can make.
