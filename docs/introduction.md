# Crossdock

The number in the system must equal the number on the shelf. Crossdock keeps that true.

Stock drifts from the shelf in four ways: a count is wrong, a movement is lost, two people take the same unit, or a parcel comes back and nobody records it. The platform closes each one:

- Every change to stock is a **movement**. A movement is written once and never edited.
- Every quantity is a **projection** of the movement log. You can delete a projection and rebuild it from the log.
- A **reservation** is decided under a row lock, so two orders cannot take the same unit.
- A **count** is compared with the shelf as it was while the count ran, so a picker moving stock does not look like a loss.
- Returns and cash on delivery close in the same ledger as the stock they move.

This book covers the concepts, how to integrate, and how to run the system. The source and the run instructions are in the repository README. The full blueprint is `CROSSDOCK_SPEC.md`.

| If you want to | Read |
|---|---|
| Learn the words the system uses | [Domain glossary](glossary.md) |
| See every kind of stock movement | [Movement catalogue](movements.md) |
| Receive events in your own system | [Events and webhooks](events.md) |
| Call the REST API | [API](api.md) |
| Run it on a host | [Deployment runbook](deployment.md) |
