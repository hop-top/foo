package commands

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"
	"hop.top/foo/internal/embed"
	kitcli "hop.top/kit/go/console/cli"
	"hop.top/kit/go/core/xdg"
)

func embedRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "embed",
		Short: "Manage local embeddings",
		Long: `Manage a local vector store for embedding and semantic search.

Add text or files to named collections, then run similarity queries
against them. Embeddings are produced by the configured provider and
persisted in the user-scoped state directory.`,
	}
	kitcli.SetHierarchical(cmd)

	cmd.AddCommand(embedTextCmd())
	cmd.AddCommand(embedFileCmd())
	cmd.AddCommand(embedSearchCmd())
	cmd.AddCommand(embedCollectionCmd())
	return cmd
}

func embedTextCmd() *cobra.Command {
	var collection string

	cmd := &cobra.Command{
		Use:   "add <text>",
		Short: "Embed text into a collection",
		Long: `Embed a literal text string and persist it in the named collection.

Each call mints a fresh ULID-keyed row in the store. The default
collection is "default"; override with --collection.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			embedder, err := embed.NewOpenAIEmbedder()
			if err != nil {
				return err
			}

			store, err := openEmbedStore()
			if err != nil {
				return err
			}

			vecs, err := embedder.Embed(cmd.Context(), []string{args[0]})
			if err != nil {
				return fmt.Errorf("embed: %w", err)
			}

			storedID, err := store.Put(embed.Embedding{
				ID:          ulid.Make().String(),
				Collection:  collection,
				ContentHash: contentHash(args[0]),
				Vector:      vecs[0],
				Metadata:    map[string]string{"text": args[0]},
			})
			if err != nil {
				return fmt.Errorf("store: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "embedded %s into %q\n", storedID, collection)
			return nil
		},
	}
	kitcli.SetSideEffect(cmd, kitcli.SideEffectWriteLocal)
	cmd.Flags().StringVarP(&collection, "collection", "c", "default", "Collection name")
	return cmd
}

func embedFileCmd() *cobra.Command {
	var (
		collection string
		filePath   string
	)

	cmd := &cobra.Command{
		Use:   "file --file <path>",
		Short: "Embed a file as chunked vectors",
		Long: `Read a file from disk, chunk it, and embed each chunk into the
named collection. Each chunk is stored with source-path and chunk-index
metadata so search results can be traced back. Re-running with the
same content produces new rows; provide --idempotency-key to dedupe.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if filePath == "" {
				return fmt.Errorf("--file is required")
			}

			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}

			chunks := embed.Chunk(string(data), nil)
			if len(chunks) == 0 {
				return nil
			}

			embedder, err := embed.NewOpenAIEmbedder()
			if err != nil {
				return err
			}
			store, err := openEmbedStore()
			if err != nil {
				return err
			}

			vecs, err := embedder.Embed(cmd.Context(), chunks)
			if err != nil {
				return fmt.Errorf("embed: %w", err)
			}

			for i, chunk := range chunks {
				_, err := store.Put(embed.Embedding{
					ID:          ulid.Make().String(),
					Collection:  collection,
					ContentHash: contentHash(chunk),
					Vector:      vecs[i],
					Metadata: map[string]string{
						"source": filePath,
						"chunk":  fmt.Sprintf("%d/%d", i+1, len(chunks)),
					},
				})
				if err != nil {
					return fmt.Errorf("store chunk %d: %w", i+1, err)
				}
			}

			fmt.Fprintf(cmd.OutOrStdout(), "embedded %d chunks from %s into %q\n", len(chunks), filePath, collection)
			return nil
		},
	}
	kitcli.SetSideEffect(cmd, kitcli.SideEffectWriteLocal)
	kitcli.SetIdempotency(cmd, kitcli.IdempotencyConditional)
	cmd.Flags().StringVarP(&collection, "collection", "c", "default", "Collection name")
	cmd.Flags().StringVar(&filePath, "file", "", "File to embed")
	return cmd
}

func embedSearchCmd() *cobra.Command {
	var (
		collection string
		count      int
	)

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search for similar embedded content",
		Long: `Embed the query string and return the top --count nearest neighbors
from the named collection. Pure read; no state mutation.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			embedder, err := embed.NewOpenAIEmbedder()
			if err != nil {
				return err
			}
			store, err := openEmbedStore()
			if err != nil {
				return err
			}

			vecs, err := embedder.Embed(cmd.Context(), []string{args[0]})
			if err != nil {
				return fmt.Errorf("embed query: %w", err)
			}

			results, err := store.Similar(collection, vecs[0], count)
			if err != nil {
				return fmt.Errorf("search: %w", err)
			}

			rows := make([]embedResultRow, 0, len(results))
			for _, item := range results {
				rows = append(rows, embedResultRow{
					ID:     shortID(item.ID),
					Score:  item.Score,
					Source: item.Metadata["source"],
					Chunk:  item.Metadata["chunk"],
				})
			}
			return renderData(cmd, rows)
		},
	}

	kitcli.SetSideEffect(cmd, kitcli.SideEffectRead)
	cmd.Flags().StringVarP(&collection, "collection", "c", "default", "Collection name")
	cmd.Flags().IntVarP(&count, "count", "n", 5, "Number of results")
	return cmd
}

func embedCollectionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "collection",
		Short: "Manage embedding collections",
		Long: `Group embeddings under a named collection.

Collections are namespaces over the local vector store. List or
delete collections; embeddings themselves are added with
'foo embed add' or 'foo embed file'.`,
	}
	kitcli.SetHierarchical(cmd)

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List collections",
		Long:  "List every collection in the local vector store with its row count.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := openEmbedStore()
			if err != nil {
				return err
			}
			collections, err := store.ListCollections()
			if err != nil {
				return err
			}
			rows := make([]collectionRow, 0, len(collections))
			for _, collection := range collections {
				count, countErr := store.CollectionCount(collection)
				if countErr != nil {
					return countErr
				}
				rows = append(rows, collectionRow{Name: collection, Count: count})
			}
			return renderData(cmd, rows)
		},
	}
	kitcli.SetSideEffect(listCmd, kitcli.SideEffectRead)
	cmd.AddCommand(listCmd)

	deleteCmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete one collection",
		Long:  "Drop every embedding stored under the named collection. Irreversible local state loss.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openEmbedStore()
			if err != nil {
				return err
			}
			if err := store.DeleteCollection(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "collection %q deleted\n", args[0])
			return nil
		},
	}
	kitcli.SetSideEffect(deleteCmd, kitcli.SideEffectDestructiveLocal)
	cmd.AddCommand(deleteCmd)

	return cmd
}

func openEmbedStore() (*embed.Store, error) {
	stateDir, err := xdg.StateDir("foo")
	if err != nil {
		return nil, fmt.Errorf("state dir: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, err
	}

	dbPath := filepath.Join(stateDir, "embeddings.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	return embed.NewStore(db)
}

func contentHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

type embedResultRow struct {
	ID     string  `json:"id" yaml:"id" table:"ID,priority=9"`
	Score  float64 `json:"score" yaml:"score" table:"SCORE,priority=8"`
	Source string  `json:"source,omitempty" yaml:"source,omitempty" table:"SOURCE,priority=7"`
	Chunk  string  `json:"chunk,omitempty" yaml:"chunk,omitempty" table:"CHUNK,priority=6"`
}

type collectionRow struct {
	Name  string `json:"name" yaml:"name" table:"NAME,priority=9"`
	Count int    `json:"count" yaml:"count" table:"COUNT,priority=8"`
}
