// `foo model list` — the discovery half of `foo model default`.
//
// Before this command the only way to learn a model id foo would accept
// was to curl a provider's /v1/models by hand: `foo provider list`
// prints compiled-in schemes and never a model id. This lists the aim
// catalog (models.dev), which aim itself caches.

package commands

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"hop.top/foo/internal/llm"
	kitcli "hop.top/kit/go/console/cli"
	"hop.top/kit/go/console/output"
)

// modelListLimit backs --limit, the display knob: how much of the
// result to show. The content filters below decide what is in the
// result in the first place, and are applied before truncation.
var modelListLimit int

// modelListFilterFlags holds the filter flag values. Capability flags
// are plain bools here and only become a tristate pointer in
// [modelListFilter], gated on whether cobra saw the flag — see there
// for why a *bool cannot be bound directly.
type modelListFilterFlags struct {
	provider         string
	family           string
	input            []string
	output           []string
	query            string
	toolCall         bool
	reasoning        bool
	openWeights      bool
	structuredOutput bool
}

// modelListFlags backs the filter flags on `foo model list`.
var modelListFlags modelListFilterFlags

// modelListEndpoint backs --endpoint: list from a live OpenAI-compatible
// server's /v1/models instead of the aim catalog.
var modelListEndpoint string

// modelListRefresh backs --refresh: bypass both caches for this run.
//
// One flag covers both because the user's question is "am I looking at
// current data", and they should not have to know that two independent
// caches with two different TTLs sit behind one listing.
var modelListRefresh bool

// modelListAll backs --all: show the whole catalog, including models
// whose provider foo has no adapter for or no credential for.
//
// It widens the candidate set rather than replacing the filters, so it
// composes with --provider, --query and the rest: `--provider groq
// --all` is the only way to see groq's catalogue before you have a key,
// and refusing the combination would leave no way to ask that question.
var modelListAll bool

// modelCatalogSource is the row producer `foo model list` reads from.
// nil means "the aim catalog through foo's shared registry". It is a
// package var so a test can substitute a fixture source without a
// network fetch, and so a second source can be selected here rather
// than by rewriting RunE.
var modelCatalogSource llm.CatalogSource

func modelListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List models from the model catalog",
		Long: `List models known to the aim catalog (models.dev), newest first.

By default only models you can actually call are listed: ones foo links
an adapter for, whose provider either needs no credential (a local
runtime) or whose API key is present in your secret store. The catalog
spans thousands of models across hundreds of providers and almost all of
them need a key you have not configured, so the unfiltered view is
mostly models that would fail on first use. --all turns the filtering
off and lists the whole catalog; a footer reports how many rows it would
add. --all composes with the filters below rather than replacing them.

The remaining view is still truncated. Models are ordered rotating
across providers so the first screenful is each provider's current
flagship rather than one aggregator's back catalogue.

Pass an id from the ID column to ` + "`foo model default`" + ` to make it the
default. Raise --limit (or --limit=0 for everything) to see more; pipe
through --format json for machine consumption.

Filters narrow the catalog before truncation, and combine with AND.
Capability filters are three-state: omit the flag for no filtering,
pass --reasoning for models that have it, --reasoning=false for models
that do not. --input and --output are repeatable and require every
listed modality. --query takes a catalog expression such as
"provider:openai reasoning:true"; where it names the same thing as an
explicit flag, the flag wins.

The catalog cannot know about a model you serve yourself. When an
endpoint is configured (` + "`providers.<scheme>.base_url`" + ` in llm.yaml, or
LLM_BASE_URL), this lists that server's own models instead; --endpoint
points at one for a single invocation. Endpoint rows carry only an id —
the /v1/models response has no cost, context window or capability data —
so the SOURCE column marks where each row came from.

Both sources are cached, on very different clocks. The catalog is a
published census that moves daily and aim caches it for 24h with ETag
revalidation; a footer line reports how old that copy is. An endpoint's
inventory is local state that changes the moment you pull a model, so it
is cached for only ` + llm.DefaultEndpointCacheTTL.String() + `. --refresh bypasses both.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runModelList(cmd.Context(), cmd, modelListLimit,
				modelListFilter(cmd, modelListFlags), modelListEndpoint,
				modelListRefresh, modelListAll)
		},
	}
	cmd.Flags().IntVar(&modelListLimit, "limit", llm.DefaultListLimit,
		"maximum models to list (0 for no limit)")

	f := cmd.Flags()
	f.StringVar(&modelListFlags.provider, "provider", "",
		"only models from this provider id (exact match)")
	f.StringVar(&modelListFlags.family, "family", "",
		"only models in this family (exact match)")
	// --in/--out rather than --input/--output: "output" is a
	// kit-reserved persistent global (write-to-path, -o), and a local
	// flag of that name masks it with no warning — the path is parsed
	// as a modality and the write silently never happens. These names
	// also match the query DSL's own keys, so `--in image` and
	// `--query 'in:image'` finally say the same word.
	f.StringArrayVar(&modelListFlags.input, "in", nil,
		"only models accepting this input modality (repeatable)")
	f.StringArrayVar(&modelListFlags.output, "out", nil,
		"only models producing this output modality (repeatable)")
	f.BoolVar(&modelListFlags.toolCall, "tool-call", false,
		"only models with (--tool-call) or without (--tool-call=false) tool calling")
	f.BoolVar(&modelListFlags.reasoning, "reasoning", false,
		"only models with (--reasoning) or without (--reasoning=false) reasoning")
	f.BoolVar(&modelListFlags.openWeights, "open-weights", false,
		"only models with (--open-weights) or without (--open-weights=false) open weights")
	f.BoolVar(&modelListFlags.structuredOutput, "structured-output", false,
		"only models with (--structured-output) or without (--structured-output=false) structured output")
	f.StringVar(&modelListFlags.query, "query", "",
		`catalog query expression, e.g. "provider:openai reasoning:true"`)
	f.StringVar(&modelListEndpoint, "endpoint", "",
		"list models from this OpenAI-compatible base URL instead of the catalog")
	f.BoolVar(&modelListRefresh, "refresh", false,
		"bypass the catalog and endpoint caches and refetch")
	f.BoolVar(&modelListAll, "all", false,
		"include models whose provider foo cannot reach (no adapter, or no API key configured)")

	kitcli.SetSideEffect(cmd, kitcli.SideEffectRead)
	return cmd
}

// tristate returns a pointer to the flag's value, or nil when the flag
// was never given.
//
// This is the whole reason the capability flags are not bound straight
// to a *bool. aim.Filter's capability fields are tristates where nil
// means "do not filter"; binding a bool and always taking its address
// would make the field non-nil on every invocation, so a bare `foo
// model list` would silently filter to models whose capability is
// false — dropping most of the catalog with nothing on screen to
// explain it. Consulting Changed is what keeps "unset" distinct from
// the explicit "=false" the flag's own default would otherwise forge.
func tristate(cmd *cobra.Command, name string, val bool) *bool {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	return &val
}

// modelListFilter assembles the filter from the parsed flags. It reads
// Changed off cmd rather than trusting the bound values, so the
// unset/true/false distinction survives.
func modelListFilter(cmd *cobra.Command, flags modelListFilterFlags) llm.Filter {
	return llm.Filter{
		Provider:         flags.provider,
		Family:           flags.family,
		Input:            flags.input,
		Output:           flags.output,
		Query:            flags.query,
		ToolCall:         tristate(cmd, "tool-call", flags.toolCall),
		Reasoning:        tristate(cmd, "reasoning", flags.reasoning),
		OpenWeights:      tristate(cmd, "open-weights", flags.openWeights),
		StructuredOutput: tristate(cmd, "structured-output", flags.structuredOutput),
	}
}

// modelFilteredSource builds a filtered row producer. It is a package
// var for the same reason modelCatalogSource is one: a test needs to
// observe the filter the command assembled without reaching the
// network. Production wiring is the aim-backed source.
//
// Filtering is pushed down into the source rather than applied to its
// rows because ModelEntry carries only a subset of the filterable
// fields — it has no family, open-weights, structured-output or
// modality — so a post-hoc pass over []ModelEntry could honour three of
// the nine filters and would have to silently ignore the rest.
var modelFilteredSource = func(f llm.Filter) llm.CatalogSource {
	return llm.NewFilteredAimCatalog(nil, f)
}

// refreshCatalog forces a catalog refetch. It is a package var for the
// same reason modelFilteredSource is one: a test must observe that the
// command reached it without a models.dev fetch. Production wiring is
// llm.RefreshCatalog against foo's shared registry.
var refreshCatalog = func(ctx context.Context) error {
	return llm.RefreshCatalog(ctx, nil)
}

// modelAuthIndex resolves which providers this machine holds
// credentials for.
//
// It delegates to providerAuthIndex rather than constructing its own
// index, which is the whole point: `foo provider show` calls the same
// var, so the two surfaces cannot report different verdicts for the
// same provider. Stubbing that one var in a test moves both.
func modelAuthIndex(ctx context.Context) (*llm.AuthIndex, error) {
	return providerAuthIndex(ctx)
}

// catalogOnlyFlags are flags whose data only the aim catalog carries.
//
// A live /v1/models response is ids and nothing else, so any filter over
// cost, context window, capability or licensing has nothing to act on.
// Rejecting the combination by name beats the two silent alternatives:
// applying the filter to zero values quietly drops every row, ignoring
// it quietly returns rows the user asked to exclude.
//
// The list is keyed by flag name and checked with Changed, so it covers
// a flag whatever its type — filter flags landing on this command later
// need only be named here.
//
// --all is absent on purpose. It does not narrow against catalog data,
// it switches off foo's own reachability filter, and that filter never
// runs on endpoint rows in the first place — so `--endpoint … --all` is
// a redundant request, not an impossible one, and rejecting it would
// punish a user for over-specifying.
var catalogOnlyFlags = []string{
	"provider",
	"family",
	"in",
	"out",
	"query",
	"tool-call",
	"reasoning",
	"open-weights",
	"structured-output",
}

// checkCatalogOnlyFlags rejects catalog-only filters combined with a
// live endpoint. It reports the first offender by name rather than
// listing all of them, so the message stays actionable.
func checkCatalogOnlyFlags(cmd *cobra.Command) error {
	for _, name := range catalogOnlyFlags {
		f := cmd.Flags().Lookup(name)
		if f != nil && f.Changed {
			return &llm.CatalogOnlyFlagError{Flag: name}
		}
	}
	return nil
}

// modelListSource picks the row producer for one invocation and reports
// the endpoint it resolved, if any.
//
// This is the selection point the CatalogSource seam exists for: RunE
// does not branch on source, it just receives one. Precedence mirrors
// the completion path so `foo model list` and `foo "hello"` never
// disagree about which server is in play — --endpoint (this
// invocation) beats the configured endpoint (llm.yaml < LLM_BASE_URL,
// resolved by kit), which beats the catalog.
//
// A source injected for tests wins outright: it stands in for whichever
// source the flags would have selected.
//
// Filters apply to the catalog only. An endpoint's /v1/models carries
// no filterable data, so the combination is rejected upstream by
// checkCatalogOnlyFlags rather than silently ignored here.
//
// refresh reaches only the endpoint branch. The catalog's refresh is not
// a different source, it is a forced refetch into the same aim cache,
// which runModelList performs before reading — see there.
func modelListSource(filter llm.Filter, endpoint string, refresh bool) (llm.CatalogSource, string) {
	if modelCatalogSource != nil {
		return modelCatalogSource, endpoint
	}
	if endpoint == "" {
		endpoint = llm.ResolveConfiguredEndpoint()
	}
	if endpoint != "" {
		return llm.NewCachedEndpointCatalog(endpoint, nil, refresh), endpoint
	}
	if filter.IsZero() {
		return nil, ""
	}
	return modelFilteredSource(filter), ""
}

// runModelList is split out of RunE so tests drive the whole path —
// source read, ranking, truncation, render, hint — against a fixture
// source and a captured writer.
func runModelList(ctx context.Context, cmd *cobra.Command, limit int, filter llm.Filter, endpoint string, refresh, all bool) error {
	src, endpoint := modelListSource(filter, endpoint, refresh)
	if endpoint != "" {
		if err := checkCatalogOnlyFlags(cmd); err != nil {
			return err
		}
	}

	// Both the forced refetch and the provenance footer below are
	// claims about aim's cache, and are only true when the rows
	// actually came from it.
	fromAimCatalog := endpoint == "" && modelCatalogSource == nil

	// The catalog's half of --refresh runs before the listing rather
	// than inside the source: aim's Refresh writes through the same
	// cache the source then reads, so forcing it here means the rows
	// below are the fresh ones, and a refetch failure is reported
	// before a single stale row has been printed.
	if refresh && fromAimCatalog {
		if err := refreshCatalog(ctx); err != nil {
			return err
		}
	}

	entries, err := llm.ListModels(ctx, src)
	if err != nil {
		return err
	}

	// Reachability filtering is a claim about catalog providers and
	// their API keys, so it applies only to catalog rows. An endpoint
	// listing is a server's own inventory: there is no catalog
	// provider behind those ids to hold a credential requirement, and
	// the server answering at all is a stronger proof of reach than
	// any key check. Filtering there would hide rows for want of a key
	// nobody asked for, so the whole block is skipped rather than
	// erroring on the combination — --endpoint --all is simply --all
	// with nothing left to widen.
	var reach reachabilityNote
	if !all && endpoint == "" {
		entries, reach, err = filterReachable(ctx, entries)
		if err != nil {
			return err
		}
	}

	shown, omitted := llm.Truncate(entries, limit)

	rows := make([]modelRow, 0, len(shown))
	for _, e := range shown {
		rows = append(rows, modelRowFromEntry(e))
	}
	// Source earns a table column only once a listing can mix origins.
	// With an endpoint in play the distinction is load-bearing: an
	// endpoint row's empty CONTEXT means "not reported", not "zero".
	payload := withSourceColumn(rows, endpoint != "")

	// Provenance is gated on the rows really coming from aim. It
	// describes the models.dev snapshot, so it says nothing useful
	// about a live endpoint — whose own cache is a five-minute detail
	// no reader needs narrated — and attaching it to an injected test
	// source would narrate a cache those rows never touched.
	//
	// Where it goes depends on the format, mirroring kit's own
	// WithProvenance rule: the structured formats nest it beside the
	// rows, the columnar ones get a stderr footer, because wrapping a
	// tagged row slice in an untagged envelope makes the table renderer
	// resolve zero columns and print nothing at all.
	footer := ""
	if fromAimCatalog {
		prov := llm.ReadCatalogProvenance(nil)
		if structuredFormat(cmd) {
			payload = catalogEnvelope{Data: payload, Meta: metaFromProvenance(prov)}
		} else {
			footer = prov.Describe()
		}
	}

	if err := renderData(cmd, payload); err != nil {
		return err
	}
	// Stderr, for the same reason as the truncation hint below: it must
	// never land in a `--format json` stream someone is parsing.
	if footer != "" {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), footer)
	}

	// The hint goes to stderr so it never contaminates `--format json`
	// piped into a parser, and is suppressed when nothing was cut.
	if omitted > 0 {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"%d more model(s) not shown; raise --limit or pass --limit=0 for all\n",
			omitted)
	}

	// Last, so it is the line still on screen after a long listing —
	// and on stderr with the others, for the same piping reason. It is
	// what keeps a filtered view honest: a reader who cannot find a
	// model they know exists has to be able to tell "foo filtered it
	// out, here is the flag" from "the catalog does not have it".
	if note := reach.describe(); note != "" {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), note)
	}
	return nil
}

// reachabilityNote is what the default view has to disclose about its
// own filtering.
//
// It is a value rather than a string built at the filter site because
// the message depends on facts the filter has and the renderer does not
// — how many rows went, and whether any credential is configured at
// all — while *where* it prints depends on facts the renderer has. The
// zero value describes an unfiltered listing and renders nothing, which
// is what --endpoint and --all leave behind.
type reachabilityNote struct {
	// hidden is how many rows the credential filter dropped.
	hidden int
	// applied records that filtering ran, distinguishing "nothing was
	// hidden" from "nothing was filtered".
	applied bool
	// configured is the providers whose API key resolved, which is
	// what turns "nothing is reachable" from a dead end into a
	// diagnosis.
	configured []string
}

// describe renders the footer, or "" when there is nothing to disclose.
func (n reachabilityNote) describe() string {
	switch {
	case !n.applied:
		return ""
	case n.hidden == 0:
		return ""
	case len(n.configured) == 0:
		// The empty-table case. An empty table with a bare count reads
		// as "the catalog is broken"; the actual cause is that no
		// provider key is configured, and saying so is the difference
		// between a dead end and a next step.
		return fmt.Sprintf(
			"%d model(s) hidden: no provider API key is configured, so foo cannot call any of them. "+
				"Set one (e.g. export OPENAI_API_KEY=…, or `foo provider show <scheme>` to see what a provider expects), "+
				"or pass --all to list the catalog anyway.",
			n.hidden)
	default:
		return fmt.Sprintf(
			"%d model(s) hidden: no adapter or no API key configured for their provider. "+
				"Configured: %s. Pass --all to list them.",
			n.hidden, strings.Join(n.configured, ", "))
	}
}

// filterReachable drops the rows foo could not call and describes what
// went. Resolving the credential state is fallible (it reads the
// provider census and the secret store), and the error is returned
// rather than degraded into "hide nothing": silently listing 7900
// unreachable models because a secret backend was unreadable is the
// behaviour this command exists to stop.
func filterReachable(ctx context.Context, entries []llm.ModelEntry) ([]llm.ModelEntry, reachabilityNote, error) {
	auth, err := modelAuthIndex(ctx)
	if err != nil {
		return nil, reachabilityNote{}, err
	}
	kept, hidden := llm.FilterReachable(entries, auth)
	return kept, reachabilityNote{
		hidden:     hidden,
		applied:    true,
		configured: auth.ConfiguredProviders(),
	}, nil
}

// catalogEnvelope wraps a listing with its cache provenance for the
// structured formats.
//
// The shape mirrors kit's own provenance envelope — {"data": …,
// "_meta": …} — so a consumer that already parses one foo command's
// envelope parses this one. It is a local type rather than
// output.WithProvenance because Dispatch takes no RenderOptions: only
// the lower-level Render does, and dropping to Render would mean
// reimplementing --output, --cols, --template and format resolution at
// this call site.
type catalogEnvelope struct {
	Data any         `json:"data"  yaml:"data"`
	Meta catalogMeta `json:"_meta" yaml:"_meta"`
}

// catalogMeta is the provenance body.
//
// Both the machine-readable facts and the rendered phrase are carried.
// A script wants fetched_at and cache_age_seconds to compare against
// its own threshold; a human reading `--format yaml` wants the same
// sentence the table footer shows, and making them re-derive it from
// two timestamps is how the two drift apart.
type catalogMeta struct {
	Source          string `json:"source"                      yaml:"source"`
	Cached          bool   `json:"cached"                      yaml:"cached"`
	FetchedAt       string `json:"fetched_at,omitempty"        yaml:"fetched_at,omitempty"`
	CacheAgeSeconds int64  `json:"cache_age_seconds,omitempty" yaml:"cache_age_seconds,omitempty"`
	TTLSeconds      int64  `json:"ttl_seconds,omitempty"       yaml:"ttl_seconds,omitempty"`
	Stale           bool   `json:"stale"                       yaml:"stale"`
	Description     string `json:"description"                 yaml:"description"`
}

// catalogSourceName names the upstream the catalog mirrors, so a
// consumer of the envelope knows what "cached" is cached *from*.
const catalogSourceName = "models.dev"

// metaFromProvenance projects foo's provenance view onto the envelope
// body. A never-fetched catalog reports cached=false with the
// timestamps omitted rather than zero-valued — a fetched_at of
// year 1 is worse than no field at all.
func metaFromProvenance(p llm.CatalogProvenance) catalogMeta {
	m := catalogMeta{
		Source:      catalogSourceName,
		Cached:      p.Fetched,
		Stale:       p.Stale,
		Description: p.Describe(),
	}
	if p.Fetched {
		m.FetchedAt = p.FetchedAt.UTC().Format(time.RFC3339)
		m.CacheAgeSeconds = int64(p.Age.Seconds())
		m.TTLSeconds = int64(p.TTL.Seconds())
	}
	return m
}

// structuredFormat reports whether the resolved output format can nest
// an envelope.
//
// kit decides this internally (output.isTagDriven) but exports neither
// the predicate nor its format resolution, so the two inputs Dispatch
// consults are read here the same way it reads them: the --format flag,
// then the extension of --output when one maps to a formatter. Getting
// this wrong is visible rather than silent — a false positive hands the
// table renderer an untagged wrapper and it prints nothing — which is
// what the format tests pin.
func structuredFormat(cmd *cobra.Command) bool {
	format := output.Table
	explicit := false
	if f := inheritedFlag(cmd, "format"); f != nil {
		explicit = f.Changed
		if v := f.Value.String(); v != "" {
			format = v
		}
	}
	// An --output extension picks the formatter only when --format was
	// not spelled out; an explicit --format wins, exactly as Dispatch
	// resolves it (and Dispatch errors on a genuine mismatch, so this
	// never has to).
	//
	// The lookup deliberately walks up to the root's persistent flags
	// rather than using cmd.Flags(). `model list` registers its own
	// local --output — the repeatable output-modality filter — which
	// shadows kit's persistent --output file path on this one command.
	// Reading cmd.Flags() here would hand filepath.Ext a modality list
	// like "[text image]" instead of a path.
	if !explicit {
		if o := inheritedFlag(cmd, "output"); o != nil {
			if ext := strings.ToLower(filepath.Ext(o.Value.String())); ext != "" {
				if mapped, ok := output.Default.ExtensionMap()[ext]; ok {
					format = mapped
				}
			}
		}
	}
	switch format {
	case output.JSON, output.YAML:
		return true
	default:
		return false
	}
}

// inheritedFlag finds a persistent flag by walking up from cmd, and is
// how the two output flags kit installs on the root are read.
//
// cmd.Flags() would be the obvious lookup and is wrong here for two
// reasons. `model list` registers its own local --output — the
// repeatable output-modality filter — which shadows kit's --output
// destination path on this one command, so cmd.Flags() would hand
// filepath.Ext a modality list like "[text image]". And the inherited
// set is only merged into cmd.Flags() once cobra has parsed, which
// makes a direct unit test of the predicate see nothing at all.
//
// Only persistent sets are consulted, so a command-local flag of the
// same name can never be mistaken for the root's.
func inheritedFlag(cmd *cobra.Command, name string) *pflag.Flag {
	for c := cmd; c != nil; c = c.Parent() {
		if f := c.PersistentFlags().Lookup(name); f != nil {
			return f
		}
	}
	return nil
}

// modelRowFromEntry projects a catalog entry onto the wire/table row.
func modelRowFromEntry(e llm.ModelEntry) modelRow {
	return modelRow{
		Provider:  e.Provider,
		ID:        e.ID,
		Context:   e.Context,
		ToolCall:  e.ToolCall,
		Reasoning: e.Reasoning,
		Released:  e.Released,
		Source:    string(e.Source),
	}
}

// modelRow is one row of `foo model list`.
//
// Table columns are the four a person picking a model needs — who
// serves it, what to type, how much context, whether tools work — plus
// reasoning and release date, which is what distinguishes near-
// identical ids within one provider. Source is carried on the
// structured formats but kept off the table: with a single source
// wired every row would repeat the same value, and it earns a column
// only once a listing can mix origins.
type modelRow struct {
	Provider  string `json:"provider" yaml:"provider" table:"PROVIDER,priority=9"`
	ID        string `json:"id" yaml:"id" table:"ID,priority=8"`
	Context   int    `json:"context" yaml:"context" table:"CONTEXT,priority=7"`
	ToolCall  bool   `json:"tool_call" yaml:"tool_call" table:"TOOLS,priority=6"`
	Reasoning bool   `json:"reasoning" yaml:"reasoning" table:"REASONING,priority=5"`
	Released  string `json:"released,omitempty" yaml:"released,omitempty" table:"RELEASED,priority=4"`
	Source    string `json:"source" yaml:"source"`
}

// sourcedModelRow is modelRow with SOURCE promoted to a table column.
//
// Column visibility is fixed in the struct tag, so making SOURCE
// conditional needs a second type rather than a runtime toggle. The
// json/yaml tags are identical to modelRow's: the structured formats
// already carried source unconditionally and must not shift shape
// depending on which source answered.
//
// SOURCE sorts below the descriptive columns but above RELEASED, which
// endpoint rows never populate.
type sourcedModelRow struct {
	Provider  string `json:"provider" yaml:"provider" table:"PROVIDER,priority=9"`
	ID        string `json:"id" yaml:"id" table:"ID,priority=8"`
	Context   int    `json:"context" yaml:"context" table:"CONTEXT,priority=7"`
	ToolCall  bool   `json:"tool_call" yaml:"tool_call" table:"TOOLS,priority=6"`
	Reasoning bool   `json:"reasoning" yaml:"reasoning" table:"REASONING,priority=5"`
	Released  string `json:"released,omitempty" yaml:"released,omitempty" table:"RELEASED,priority=3"`
	Source    string `json:"source" yaml:"source" table:"SOURCE,priority=4"`
}

// withSourceColumn returns rows carrying a SOURCE table column when the
// listing can mix origins, and the plain rows otherwise. Returning `any`
// keeps the choice of row type at this one call site.
func withSourceColumn(rows []modelRow, show bool) any {
	if !show {
		return rows
	}
	out := make([]sourcedModelRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, sourcedModelRow(r))
	}
	return out
}
