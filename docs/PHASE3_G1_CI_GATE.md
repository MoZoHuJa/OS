# Phase 3 G1 — Final CI Gate

This file exists to make the final candidate state explicit for CI review.

The latest candidate must pass the repository's complete GitHub Actions workflow, including:
- Go formatting, vet, build, and all tests;
- race tests for allocation, inventory, and reservation;
- offline smoke and configuration validation;
- Docker build and API integration tests.

Passing automated checks does not replace the target-host crash-injection and real cleanup-verifier tests listed in `PHASE3_G1_ACCEPTANCE.md`.
