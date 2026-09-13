# Monorepo Layout and Distribution Strategy

We need a unified hub to develop custom CLIProxyAPI plugins and distribute trusted external plugins to Kubernetes deployments through a single store source. We decided on an independent-manifest monorepo structure where in-tree plugins reside in `plugins/<plugin-id>/`, external plugins are declared in `external/<plugin-id>.json`, and a local tool compiles them into a root `registry.json`. Independent git tags (`<plugin-id>/v<version>`) trigger dedicated CI builds for each plugin. External plugins support direct release referencing with an optional in-tree build fallback when self-hosted compilation is required.

## Status

accepted

## Considered Options

- Unified monorepo versioning with a single shared release tag for all plugins.
- Direct manual editing of a single monolithic `registry.json` without sub-manifests.
- Git Submodules for vendoring external plugin source code.

## Consequences

- Consumers configure only one store source URL in `plugins.store-sources`.
- Each plugin maintains an independent release lifecycle and clear dependency boundaries.
- Adding or removing plugins requires only modifying dedicated directory files without merge conflicts.
