# CPA Plugin Distribution Hub

A central repository for aggregating, building, and publishing CLIProxyAPI plugins to a unified registry.

## Language

**In-Tree Plugin**:
A plugin whose source code is authored, versioned, and built directly within this repository under `plugins/`.
_Avoid_: Builtin plugin, internal module, native plugin

**External Plugin**:
A trusted third-party plugin whose source code is hosted in an external repository, declared in `external/` either by direct release reference or by self-hosted build recipe.
_Avoid_: Remote plugin, third-party module

**Plugin Registry**:
The aggregated `registry.json` catalog consumed by CLIProxyAPI through `plugins.store-sources`.
_Avoid_: Store index, plugin list, repository catalog

**Plugin Artifact**:
A platform-specific zip archive containing the compiled native dynamic library attached to a GitHub Release.
_Avoid_: Binary package, dynamic link library, release bundle
