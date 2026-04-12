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
	"hop.top/kit/xdg"
)

var embedCollection string

func embedCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "embed <text>",
		Short: "Embed text into a vector collection",
		Args:  cobra.ExactArgs(1),
		RunE:  runEmbed,
	}
	cmd.Flags().StringVarP(&embedCollection, "collection", "c", "default", "Collection name")
	return cmd
}

func embedMultiCmd() *cobra.Command {
	var filePath string

	cmd := &cobra.Command{
		Use:   "embed-multi",
		Short: "Embed a file as chunked vectors",
		RunE: func(cmd *cobra.Command, args []string) error {
			if filePath == "" {
				return fmt.Errorf("--file is required")
			}

			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}

			chunks := embed.Chunk(string(data), nil)
			if len(chunks) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No chunks generated")
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

			ctx := cmd.Context()
			vecs, err := embedder.Embed(ctx, chunks)
			if err != nil {
				return fmt.Errorf("embed: %w", err)
			}

			out := cmd.OutOrStdout()
			for i, chunk := range chunks {
				hash := contentHash(chunk)
				id := ulid.Make().String()
				_, err := store.Put(embed.Embedding{
					ID:          id,
					Collection:  embedCollection,
					ContentHash: hash,
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
			fmt.Fprintf(out, "Embedded %d chunks from %s into %q\n",
				len(chunks), filePath, embedCollection)
			return nil
		},
	}

	cmd.Flags().StringVarP(&embedCollection, "collection", "c", "default", "Collection name")
	cmd.Flags().StringVar(&filePath, "file", "", "File to embed")
	return cmd
}

func similarCmd() *cobra.Command {
	var n int

	cmd := &cobra.Command{
		Use:   "similar <query>",
		Short: "Find similar embeddings by cosine similarity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			embedder, err := embed.NewOpenAIEmbedder()
			if err != nil {
				return err
			}

			store, err := openEmbedStore()
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			vecs, err := embedder.Embed(ctx, []string{args[0]})
			if err != nil {
				return fmt.Errorf("embed query: %w", err)
			}

			results, err := store.Similar(embedCollection, vecs[0], n)
			if err != nil {
				return fmt.Errorf("search: %w", err)
			}

			out := cmd.OutOrStdout()
			if len(results) == 0 {
				fmt.Fprintln(out, "No results found")
				return nil
			}

			for _, r := range results {
				source := r.Metadata["source"]
				chunk := r.Metadata["chunk"]
				fmt.Fprintf(out, "%.4f  %s", r.Score, r.ID)
				if source != "" {
					fmt.Fprintf(out, "  [%s", source)
					if chunk != "" {
						fmt.Fprintf(out, " %s", chunk)
					}
					fmt.Fprint(out, "]")
				}
				fmt.Fprintln(out)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&embedCollection, "collection", "c", "default", "Collection name")
	cmd.Flags().IntVarP(&n, "count", "n", 5, "Number of results")
	return cmd
}

func collectionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "collections",
		Short: "Manage embedding collections",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List collections",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openEmbedStore()
			if err != nil {
				return err
			}

			cols, err := store.ListCollections()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if len(cols) == 0 {
				fmt.Fprintln(out, "No collections")
				return nil
			}

			for _, c := range cols {
				count, _ := store.CollectionCount(c)
				fmt.Fprintf(out, "%s (%d embeddings)\n", c, count)
			}
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a collection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openEmbedStore()
			if err != nil {
				return err
			}

			if err := store.DeleteCollection(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Collection %q deleted\n", args[0])
			return nil
		},
	})

	return cmd
}

func runEmbed(cmd *cobra.Command, args []string) error {
	text := args[0]

	embedder, err := embed.NewOpenAIEmbedder()
	if err != nil {
		return err
	}

	store, err := openEmbedStore()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	vecs, err := embedder.Embed(ctx, []string{text})
	if err != nil {
		return fmt.Errorf("embed: %w", err)
	}

	hash := contentHash(text)
	id := ulid.Make().String()
	storedID, err := store.Put(embed.Embedding{
		ID:          id,
		Collection:  embedCollection,
		ContentHash: hash,
		Vector:      vecs[0],
		Metadata:    map[string]string{"text": text},
	})
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Embedded %s into %q\n", storedID, embedCollection)
	return nil
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

func contentHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])[:16]
}
