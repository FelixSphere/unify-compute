## What

<!-- What does this change do, in a sentence or two? -->

## Why

<!-- Why is this change needed? Link an issue or ADR if there is one. -->

## Licence check

<!-- Any new dependency? Name it and its licence, and confirm THIRD-PARTY-LICENSES.md is updated. Confirm nothing was copied from unifyapi-console. -->

## Testing

<!-- How did you verify this? -->

- [ ] `npx oxlint app lib` and `npm run build` pass (site changes)
- [ ] `go vet ./... && go test ./...` pass in `compute-api/` (service changes)
- [ ] `make compose-config tf-validate` pass (deploy changes)
- [ ] Checked in a browser (for UI changes)

## Release notes for the CI/CD agent

<!-- Anything the release needs in a particular order: an SSM parameter, a seed, a Terraform variable bump, a console release that must ship first. "None" is a valid answer. -->
