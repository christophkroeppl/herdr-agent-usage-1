# Hermes Agent data contract

Agent Usage reads the active Hermes profile selected by `HERMES_HOME`, or `~/.hermes` when unset. It opens `state.db` read-only and resolves only the exact Herdr `agent_session.value` against `sessions.id`. It never guesses by working directory, so concurrent panes in one checkout remain isolated.

Current context comes from `model_config._usage_anchor` only when the stored active-message prefix still matches its role and SHA-256 fingerprints. The anchor's prompt and completion counts are exact; only messages appended after the anchored response are estimated at one token per four serialized bytes. Stale or unverifiable anchors do not turn lifetime session totals into context.

The context window uses an explicit `context_length` in session model config, then `context_length_cache.yaml` entries keyed by exact `model@base_url`, with a provider-prefixed model's conservative bare-model alias. Unknown windows show an absolute context count.

Cache hit rate is cumulative: `cache_read_tokens / (input_tokens + cache_read_tokens + cache_write_tokens)`. Session burn is `input + cache read + cache write + output`; `reasoning_tokens` is not added because Hermes output already contains it. Cost prefers positive `actual_cost_usd`, then `estimated_cost_usd`. Backend identity prefers `billing_provider`, preserving values such as `llm-rosetta`, and otherwise derives a host label from `billing_base_url`.

Hermes `chat_completions`, official-model-API, and published-price routes are pay-as-you-go; `subscription_included` is plan-covered. The adapter performs that translation itself and reports the shared billing classification, so no shared layer reads a Hermes billing string. Hermes does not persist a rolling subscription quota contract, so this integration never fabricates a quota block. Optional columns are detected at runtime for compatibility with older Hermes databases, and a persisted empty string is kept distinct from NULL because Hermes's fingerprint includes any field that is not `None`.
