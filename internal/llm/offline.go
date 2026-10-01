package llm

import (
	"errors"
	"fmt"
	"net/url"

	llmerrors "hop.top/kit/go/ai/llm/errors"
	"hop.top/kit/go/console/output"
	"hop.top/kit/go/core/netpolicy"

	"hop.top/foo/internal/exitcode"
)

// CodeOffline is the error code of a model call --offline refused
// because the provider's endpoint is not local. It pairs with
// exitcode.Offline, so scripts can tell it apart from a missing API
// key (kit's unauthorized class); running the same invocation again
// cannot succeed.
const CodeOffline = "OFFLINE"

// OfflineRefusal turns a model request kit's network guard refused
// under --offline into foo's OFFLINE envelope. Any other error, nil
// included, is returned untouched. The run path applies it to every
// completion; a caller that speaks to a provider over its own HTTP
// client (the embedder) applies it to that client's errors.
//
// The guard (netpolicy, installed by cli.New) decides what is local:
// loopback addresses, the name localhost, and unix sockets. A DNS name
// is remote even when it resolves to loopback, since resolving it would
// itself touch the network. foo does not repeat that decision; it only
// names the endpoint and the way out.
func OfflineRefusal(err error) error {
	cause := findOffline(err)
	if cause == nil {
		return err
	}
	endpoint := "the model provider"
	var ue *url.Error
	if errors.As(cause, &ue) {
		if u, perr := url.Parse(ue.URL); perr == nil && u.Host != "" {
			endpoint = u.Host
		}
	}
	// Retain both: err for whatever the caller matches on, cause so
	// errors.Is(…, netpolicy.ErrOffline) holds through a fallback chain.
	retained := err
	if cause != err {
		retained = errors.Join(err, cause)
	}
	e := output.WrapError(retained, CodeOffline, exitcode.Offline).
		WithTransience(output.TransiencePermanent)
	e.Message = fmt.Sprintf("--offline refused %s: only loopback endpoints (localhost, 127.0.0.0/8, ::1) are reachable offline", endpoint)
	e.Cause = cause.Error()
	e.SuggestedFix = "use a model served on loopback (LLM_BASE_URL=http://127.0.0.1:<port>/v1, providers.<scheme>.base_url, or -m '<model>?base_url=...'), or drop --offline"
	return e
}

// findOffline returns the error in err's tree that is kit's offline
// refusal, or nil. kit's fallback chain reports every provider's
// failure in ErrFallbackExhausted.Errors but does not unwrap to them,
// so errors.Is alone never sees a refusal behind it.
func findOffline(err error) error {
	if err == nil {
		return nil
	}
	var exhausted *llmerrors.ErrFallbackExhausted
	if errors.As(err, &exhausted) {
		for _, e := range exhausted.Errors {
			if c := findOffline(e); c != nil {
				return c
			}
		}
	}
	if errors.Is(err, netpolicy.ErrOffline) {
		return err
	}
	return nil
}
