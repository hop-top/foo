package commands

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"hop.top/foo/internal/embed"
	"hop.top/foo/internal/schema"
	kitcli "hop.top/kit/go/console/cli"
	"hop.top/kit/go/console/output"
	"hop.top/kit/go/runtime/sideeffect"
)

// --dry-run is kit's global flag. kit's tier policy lets it through on
// every write or destructive leaf, tags the context, and skips the
// confirmation a destructive leaf would otherwise need — so a leaf
// that runs on under the tag acts unconfirmed. Each such leaf checks
// isDryRun after validating its input and, when set, returns
// renderPlan instead of acting. A leaf that cannot preview opts out
// with kitcli.OptOutDryRun, which makes kit refuse the flag.

// isDryRun reports whether kit tagged this invocation as a dry run.
func isDryRun(cmd *cobra.Command) bool {
	return sideeffect.IsDryRun(cmd.Context())
}

// renderPlan prints what the leaf would do, in --format json or yaml
// when asked and as kit's plan table otherwise.
func renderPlan(cmd *cobra.Command, args map[string]any, prereqs []string, effects ...kitcli.Effect) error {
	if effects == nil {
		effects = []kitcli.Effect{}
	}
	format := output.Table
	switch f := root.Viper.GetString("format"); f {
	case output.JSON, output.YAML:
		format = f
	}
	return output.RenderPlan(cmd.OutOrStdout(), format, kitcli.Plan{
		Command:              cmd.CommandPath(),
		Args:                 args,
		Effects:              effects,
		PrerequisitesChecked: prereqs,
		GeneratedAt:          time.Now().UTC(),
	})
}

// patternWriteEffect is the write pattern create and import would make:
// a new file, or a replacement that loses the old body.
func patternWriteEffect(path, name string) kitcli.Effect {
	if _, err := os.Stat(path); err == nil {
		return kitcli.Effect{Kind: "update", Target: path, Detail: fmt.Sprintf("replace pattern %q", name)}
	}
	return kitcli.Effect{Kind: "create", Target: path, Reversible: true, Detail: fmt.Sprintf("create pattern %q", name)}
}

// openReadOnly opens an existing SQLite file read-only for a dry run's
// checks: unlike the stores' own openers it never creates, migrates or
// backs up the file. ok is false when there is no file yet.
func openReadOnly(path string) (db *sql.DB, ok bool, err error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err = sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, false, err
	}
	return db, true, nil
}

// planSchemaCreate checks the input exactly as schema create does and
// previews the write.
func planSchemaCreate(cmd *cobra.Command, name, filePath string, args []string) error {
	planArgs := map[string]any{"name": name}
	switch {
	case filePath != "":
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read file: %w", err)
		}
		var obj map[string]any
		if err := json.Unmarshal(data, &obj); err != nil {
			return fmt.Errorf("invalid JSON schema: %w", err)
		}
		planArgs["file"] = filePath
	case len(args) < 2:
		return fmt.Errorf("provide DSL string or --file")
	default:
		if _, err := schema.CompileDSL(args[1]); err != nil {
			return fmt.Errorf("compile dsl: %w", err)
		}
		planArgs["dsl"] = args[1]
	}

	path, err := schemaDBPath()
	if err != nil {
		return err
	}
	exists, err := schemaExists(path, name)
	if err != nil {
		return err
	}
	effect := kitcli.Effect{Kind: "create", Target: path, Reversible: true, Detail: fmt.Sprintf("store schema %q", name)}
	if exists {
		effect = kitcli.Effect{Kind: "update", Target: path, Detail: fmt.Sprintf("replace schema %q", name)}
	}
	return renderPlan(cmd, planArgs, []string{"schema-valid"}, effect)
}

// planSchemaDelete refuses a missing schema as schema delete does and
// previews the removal.
func planSchemaDelete(cmd *cobra.Command, name string) error {
	path, err := schemaDBPath()
	if err != nil {
		return err
	}
	exists, err := schemaExists(path, name)
	if err != nil {
		return err
	}
	if !exists {
		return output.NotFoundError(fmt.Sprintf("schema %q not found", name))
	}
	return renderPlan(cmd, map[string]any{"name": name}, []string{"schema-exists"},
		kitcli.Effect{Kind: "delete", Target: path, Detail: fmt.Sprintf("remove schema %q", name)})
}

func schemaExists(path, name string) (bool, error) {
	db, ok, err := openReadOnly(path)
	if err != nil || !ok {
		return false, err
	}
	defer db.Close()
	store, err := schema.NewStore(db)
	if err != nil {
		return false, err
	}
	_, err = store.Get(name)
	return err == nil, nil
}

// planEmbed previews embed add and embed file: one embedding-model
// call, one stored row per text.
func planEmbed(cmd *cobra.Command, collection string, args map[string]any, texts []string) error {
	path, err := embedDBPath()
	if err != nil {
		return err
	}
	effects := make([]kitcli.Effect, 0, len(texts))
	for i, text := range texts {
		detail := fmt.Sprintf("embed %d bytes into collection %q", len(text), collection)
		if len(texts) > 1 {
			detail = fmt.Sprintf("embed chunk %d/%d (%d bytes) into collection %q", i+1, len(texts), len(text), collection)
		}
		effects = append(effects, kitcli.Effect{Kind: "create", Target: path, Detail: detail})
	}
	return renderPlan(cmd, args, []string{"embedding-credentials"}, effects...)
}

// planCollectionDelete previews dropping a collection. A collection
// with no rows is no change, as the real delete makes none.
func planCollectionDelete(cmd *cobra.Command, collection string) error {
	path, err := embedDBPath()
	if err != nil {
		return err
	}
	count := 0
	db, ok, err := openReadOnly(path)
	if err != nil {
		return err
	}
	if ok {
		defer db.Close()
		store, err := embed.NewStore(db)
		if err != nil {
			return err
		}
		if count, err = store.CollectionCount(collection); err != nil {
			return err
		}
	}
	var effects []kitcli.Effect
	if count > 0 {
		effects = append(effects, kitcli.Effect{Kind: "delete", Target: path,
			Detail: fmt.Sprintf("drop %d embeddings in collection %q", count, collection)})
	}
	return renderPlan(cmd, map[string]any{"name": collection}, nil, effects...)
}

// planFragmentCreate checks the source as fragment create would,
// without reading stdin or fetching a URL, and previews the write.
func planFragmentCreate(cmd *cobra.Command, args []string) error {
	alias := args[0]
	source := "stdin"
	switch {
	case len(args) == 2 && (strings.HasPrefix(args[1], "http://") || strings.HasPrefix(args[1], "https://")):
		source = args[1]
	case len(args) == 2:
		if _, err := os.Stat(args[1]); err != nil {
			return fmt.Errorf("read file: %w", err)
		}
		source = args[1]
	default:
		if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			return fmt.Errorf("no source provided and stdin is a terminal; pipe content or provide a source argument")
		}
	}
	return renderPlan(cmd, map[string]any{"alias": alias, "source": source}, nil, kitcli.Effect{
		Kind: "create", Target: "fragment:" + alias,
		Detail: "store the content of " + source + " under the alias in the workspace store",
	})
}

// planUpgrade checks for a release and previews the binary swap. The
// check refreshes kit's release-check cache, as every update check
// does; nothing else is written.
func planUpgrade(cmd *cobra.Command) error {
	r := newUpgradeChecker().Check(cmd.Context())
	if r.Err != nil {
		return fmt.Errorf("upgrade check: %w", r.Err)
	}
	var effects []kitcli.Effect
	if r.UpdateAvail {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate foo executable: %w", err)
		}
		effects = append(effects, kitcli.Effect{Kind: "update", Target: exe,
			Detail: fmt.Sprintf("replace foo %s with %s", r.Current, r.Latest)})
	}
	return renderPlan(cmd, map[string]any{"current": r.Current, "latest": r.Latest}, []string{"release-check"}, effects...)
}
