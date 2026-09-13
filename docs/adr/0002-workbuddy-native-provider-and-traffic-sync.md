# WorkBuddy Native Provider Architecture and Traffic-Driven Field Sync

We need to port the core capabilities of `pi-codebuddy-provider` into CLIProxyAPI while maintaining exact protocol fidelity with official WorkBuddy clients. We decided to implement `plugins/workbuddy` as a first-class Native Provider Plugin (implementing `AuthProvider`, `ModelProvider`, `ProviderExecutor`, and `QuotaProvider`), delegate multi-account persistence and round-robin scheduling entirely to the CPA host, and establish a traffic-driven synchronization loop using development audit scripts against real client captures. All growth events, check-ins, and private SMS registration flows are discarded in favor of a pure, minimal proxy runtime.

## Status

accepted

## Considered Options

- Implementing as a lightweight middleware interceptor (`RequestInterceptor` / `StreamChunkInterceptor`) instead of a full native provider.
- Maintaining an internal account pool and custom scheduler inside the plugin process.
- Retaining daily check-in and task reporting within the proxy request flow.
- Manual inspection of network traffic without an automated field diff verification tool.

## Consequences

- WorkBuddy models appear as native upstream targets in CPA alongside standard providers, with native OAuth QR-code login and automatic token refresh.
- Zero extra state management overhead: the plugin is stateless and relies on the CPA host for credential storage, health checks, and cooldowns.
- The public plugin repository remains completely clean of non-essential registration scripts, growth tasks, and private reverse-engineering tools.
- A dedicated audit script in the development repository explicitly checks all plugin request fields against real client traffic, guaranteeing protocol alignment before code changes.
- Scope is fixed at four capabilities: chat completions, authentication, quota, and the model list. Desktop-only surfaces such as conversation sync, cloud drive, MCP gateway, and local proxy registration are out.
- The plugin emits no telemetry. The desktop client's `/v2/report`, `/v1/traces`, and growth endpoints are deliberately not reproduced, because they change the account's server-side profile without serving the proxy.
- The plugin performs no application-level retries and no failover of its own; credential rotation, cooldowns, and recovery belong to the host, so a failed call is surfaced as an error rather than replayed.
- Dynamic model discovery (`model.for_auth`) is not implemented. The static manifest is the single source of truth for the model list.
- Prompt sanitization (rewriting Claude self-references in the system message) is new code, not a port. Upstream only ships it as a draft rule inside its static export script. The rewrite happens inside the plugin while it builds the upstream request body.
- Logging is limited to metadata: method, path, status, latency, model. Request bodies, reply text, and authorization headers are never logged. Captured sessions carry live bearer tokens, so log boundaries are a correctness requirement rather than a preference.
