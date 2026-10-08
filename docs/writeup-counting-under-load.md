# A cycle count cannot be compared to a number that keeps moving

A cycle count has one job: find out whether the number in the system matches what is on the shelf. The trouble starts when the shelf is busy. An operator walks to bay 12 to count a product while three pickers take units from it. By the time the count is entered, the system says 40, the operator counted 43, and nobody has lost anything.

Most systems get this wrong in one of two ways. Some freeze the location during the count, which stops the pickers. Others compare the count with the system quantity at submit time, which flags every busy shelf as shrinkage. In Crossdock a "shrinkage" classification posts an adjustment and goes on a supervisor's review list, so false positives are expensive. They train people to ignore the review list.

This is how Crossdock decides that a difference is timing and not loss, and where that decision nearly went wrong.

## The rule: a count is right if it was true at some moment

The operator counts blind. The sheet lists the products to look for and hides the quantities, so the count is not pulled toward what the system expects. While they count, movements keep committing against that location.

A count is plausible if the number reported equals the shelf quantity at some moment during the count. The shelf quantity at the start is known. Each movement that commits during the window either happened before the operator looked at that product or after. So the set of believable counts is the start quantity plus the effect of any subset of the in-flight movements.

That is a subset-sum check, and `Explains` in `internal/counting/classify.go` does exactly that. Three pickers who took 2, 3 and 2 units from a shelf holding 40 make 40, 38, 37, 36, 35 and 33 believable counts. 43 is not. It needs a recount.

The classes follow from that:

- Count equals the end quantity: matched, nothing to record.
- Count is believable but different: **timing**. No recount, no adjustment.
- Count is not believable: **unresolved**, and a recount task is created.
- Recount returns the system quantity: the first count was a **miscount**.
- Recount repeats the first count, below the system: **shrinkage**. Above it: **found**.
- Recount finishes more than an hour after the first: unresolved, because too much has moved to trust either count.

Each rule is a pure function with a table test. The interesting part is not the rules but what "in flight" means.

## The bug the design avoids: sequence numbers lie

The obvious way to define "movements that committed during the count" is by sequence number. Record the highest `seq` when the count starts, and everything above it happened during the count.

That is wrong in PostgreSQL. A `BIGSERIAL` value is assigned when the row is inserted, not when the transaction commits. Transaction A can take seq 100, transaction B takes 101, B commits, the count starts and records 101, and then A commits. Movement 100 is now visible, was committed after the count started, and sits below the cutoff. The count treats it as already in the baseline, the operator's count correctly excludes it or includes it depending on timing, and the arithmetic is off by that movement's quantity. In practice it would appear as a rare false variance under load that nobody could reproduce.

The fix is to ask the database what was visible, not to infer it from numbers. Each movement stores `xid xid8 DEFAULT pg_current_xact_id()`. When a count starts, it records `pg_current_snapshot()`. The baseline is every movement that was visible in that snapshot, and the window is every movement that was not, using `pg_visible_in_snapshot(xid, snapshot)`. Visibility is exactly the property we want, and PostgreSQL already tracks it.

There is a second snapshot at submit time for the end state. Both are plain text columns on the count row, so a classification can be explained later from stored facts.

## Proving the rule is not too generous

A timing rule that explains everything is as useless as one that explains nothing. If "believable" were wide enough, a real loss would hide inside the noise of busy picking. The tests cover both sides.

**No false shrinkage.** 100 pickers confirm picks across 10 locations while cycle counts run on the same locations. Every count either agrees with the system or is classified timing. Last run: 246 counts, 216 keys matched exactly, 30 variances, all timing, no recounts requested, no adjustments posted.

**No hidden loss.** The same setup, with one counter who is really one unit short on every count. Picks are always two units, so a one unit loss can never look like a pick. Every count must be unresolved and ask for a recount, and none may be called timing. They are, and none are.

I added the second test because the first alone proves little. If the rule were so generous that it explained everything, the first test would pass too. Picks of two units against a loss of one are what make "zero false shrinkage" a statement about the rule.

## What it costs, and what it does not cover

The subset-sum search doubles its reachable set for each in-flight movement, so it is capped. Past 64 movements for one product at one location during one count, only the sequential states are tried (after the first movement, after the second, and so on). Beyond the cap a believable count could be called unresolved, which costs a recount and never costs a false adjustment. The failure direction was chosen on purpose.

It also does not cover a counter who is wrong in exactly the way an in-flight movement would explain. If three units were picked and the operator is three units short, the count is classified as timing. The recount window, the supervisor confirmation step and the ageing buckets exist to catch the pattern when it repeats, not the single instance. A count is evidence, not proof.

The spec asked for 20 locations; the test uses 10. The mechanism does not depend on the number, and the 20 location run is a constant change, but I have not run it, so I will not claim it.

## Two lessons that carry over

When a rule depends on what had happened by some moment, store the snapshot and ask the database. Anything inferred from ids or timestamps has a window where concurrency makes it false. And a test that shows a bad outcome never happens needs a mirror test where the bad outcome is real, or you cannot tell the rule from a broken detector.
