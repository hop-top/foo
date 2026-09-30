package commands

import (
	"log/slog"

	"hop.top/kit/go/storage/secret"
)

// secretStore is foo's configured secret store (config `secrets:`,
// with --profile/--instance applied to its service), and the one place
// provider key lookups get it: the run's precheck through
// llm.ClientOpts.Secrets, and `foo provider show` / `foo model list`
// through providerAuthIndex. One source is what keeps a stored key from
// reading "configured" on one surface and "missing" on the other.
//
// A store foo cannot open — a backend this build does not link, or one
// missing its settings — is reported and dropped: lookups then read env
// vars only, as they did before a store was consulted at all, rather
// than failing every run over a key that may well be exported.
func secretStore() secret.Store {
	store, err := cfg.SecretStore()
	if err != nil {
		slog.Warn(
			"secrets.store.unavailable: cannot open the configured secret store; reading provider keys from env vars only",
			slog.String("backend", cfg.Secrets.Backend),
			slog.Any("err", err),
		)
		return nil
	}
	return store
}
